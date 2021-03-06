package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"time"

	"github.com/ontio/ontology-crypto/keypair"
	sdk "github.com/ontio/ontology-go-sdk"
	"github.com/ontio/ontology-go-sdk/utils"
	"github.com/ontio/ontology/common"
	"github.com/ontio/ontology/common/log"
	"github.com/ontio/ontology/core/store/leveldbstore"
	"github.com/ontio/ontology/core/types"
	utils2 "github.com/ontio/ontology/core/utils"
	"github.com/ontio/ontology/merkle"
)

type DBKey byte

const (
	KEY_SERVER_STATE DBKey = 0x1
)

const (
	walletname = "wallet.dat"
)

const (
	SERVER_STATE_INIT           uint32 = 1
	SERVER_STATE_DEPLOY_SUCCESS uint32 = 2
	SERVER_STATE_CONTRACT_INIT  uint32 = 3
	SERVER_STATE_CONFIG_RUN     uint32 = 4
)

func GetOtherKeyByHash(key DBKey) []byte {
	sink := common.NewZeroCopySink(nil)
	sink.WriteByte(byte(key))
	sink.WriteHash(merkle.EMPTY_HASH)
	return sink.Bytes()
}

func GetStateFromBytes(data []byte) (uint32, string, error) {
	source := common.NewZeroCopySource(data)
	res, eof := source.NextUint32()
	if eof {
		return 0, "", io.ErrUnexpectedEOF
	}

	hexAddress, _, irregular, eof := source.NextString()
	if irregular || eof {
		return 0, "", io.ErrUnexpectedEOF
	}

	return res, hexAddress, nil
}

type ServerConfig struct {
	Walletname        string   `json:"walletname"`
	OntNode           string   `json:"ontnode"`
	SignerAddress     string   `json:"signeraddress"`
	ServerPort        int      `json:"serverport"`
	GasPrice          uint64   `json:"gasprice"`
	CacheTime         uint32   `json:"cachetime"`
	BatchNum          uint32   `json:"batchnum"`
	TryChainInterval  uint32   `json:"trychaininterval"`
	SendTxInterval    uint32   `json:"sendtxinterval"`
	SendTxSize        uint32   `json:"sendtxsize"`
	BatchAddSleepTime uint32   `json:"batchaddsleeptime"`
	ContracthexAddr   string   `json:"contracthexaddr"`
	Authorize         []string `json:"authorize"`
}

type WitnessConfig struct {
	AuthPubKey      []string `json:"authpubkey"`
	TenantId        string   `json:"tenant_id"`
	WalletName      string   `json:"wallet_name"`
	NetType         string   `json:"nettype"`
	ContractAddress string   `json:"contractAddress"`
}

type ConfigServer struct {
	Signer       *sdk.Account
	OntSdk       *sdk.OntologySdk
	OwnerAddr    string
	ServerConfig *ServerConfig
	State        uint32
	InitTx       *types.MutableTransaction
	VerifyTx     *types.MutableTransaction
	DB           *leveldbstore.LevelDBStore
}

func NewConfigServer(levelDBName string, fixedConfigPath string, witnessConfigPath string, prefixRunDir string) (*ConfigServer, error) {
	var fixedConfig ServerConfig
	var witnessConfig WitnessConfig
	var configServer ConfigServer
	var hexAddress string
	var err error

	// db init.
	configServer.DB, err = leveldbstore.NewLevelDBStore(levelDBName)
	if err != nil {
		return nil, fmt.Errorf("NewConfigServer DB: %s", err)
	}

	data, err := configServer.DB.Get(GetOtherKeyByHash(KEY_SERVER_STATE))
	if err != nil {
		// must already deployed
		configServer.State = SERVER_STATE_DEPLOY_SUCCESS
	} else {
		configServer.State, hexAddress, err = GetStateFromBytes(data)
	}

	log.Infof("Server state %d", configServer.State)

	// witniess config fill
	buffTenant, err := ioutil.ReadFile(witnessConfigPath)
	err = json.Unmarshal([]byte(buffTenant), &witnessConfig)
	if err != nil {
		return nil, fmt.Errorf("NewConfigServer witnessConfig: %s", err)
	}

	log.Infof("config witiness %v", &witnessConfig)

	AuthAddrs := make([]string, 0)
	for _, pub := range witnessConfig.AuthPubKey {
		raw, err := common.HexToBytes(pub)
		if err != nil {
			return nil, fmt.Errorf("NewConfigServer AddressFromBase58: %s", err)
		}
		pubkey, err := keypair.DeserializePublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("NewConfigServer AddressFromBase58: %s", err)
		}
		addr := types.AddressFromPubKey(pubkey)
		AuthAddrs = append(AuthAddrs, addr.ToBase58())
	}

	// fixed config fill
	buffFixed, err := ioutil.ReadFile(fixedConfigPath)
	if err != nil {
		return nil, fmt.Errorf("NewConfigServer: %s", err)
	}

	err = json.Unmarshal([]byte(buffFixed), &fixedConfig)
	if err != nil {
		return nil, fmt.Errorf("NewConfigServer: %s", err)
	}

	var ismainnet bool

	if witnessConfig.NetType == "testnet" {
		log.Infof("nettype: testnet")
		fixedConfig.OntNode = "http://polaris2.ont.io:20336"
		ismainnet = false
	} else if witnessConfig.NetType == "mainnet" {
		log.Infof("nettype: mainnet")
		fixedConfig.OntNode = "http://dappnode2.ont.io:20336"
		ismainnet = true
	} else {
		return nil, fmt.Errorf("NewConfigServer wrong nettype :%s", witnessConfig.NetType)
	}

	log.Infof("config fixed %v", &fixedConfig)

	// update AuthAddr. only ContracthexAddr not init
	ontSdk := sdk.NewOntologySdk()
	ontSdk.NewRpcClient().SetAddress(fixedConfig.OntNode)
	fixedConfig.Authorize = append(fixedConfig.Authorize, AuthAddrs...)

	configServer.OntSdk = ontSdk
	configServer.ServerConfig = &fixedConfig
	configServer.ServerConfig.ContracthexAddr = witnessConfig.ContractAddress
	if hexAddress != "" && witnessConfig.ContractAddress != hexAddress {
		return nil, fmt.Errorf("contract address already init to %s. not %s", hexAddress, witnessConfig.ContractAddress)
	}

	if configServer.ServerConfig.ContracthexAddr == "" {
		return nil, fmt.Errorf("contract address can not init to empty.")
	}

	wallresp, err := RequestForWallet(ismainnet, witnessConfig.WalletName)
	if err != nil {
		return nil, err
	}

	configServer.ServerConfig.Walletname = walletname
	sigwalletplace := prefixRunDir + walletname
	err = ioutil.WriteFile(sigwalletplace, []byte(wallresp.Content), 0644)
	if err != nil {
		log.Errorf("write wallet err: %s", err)
		return nil, err
	}

	// init signer.
	wallet, err := ontSdk.OpenWallet(sigwalletplace)
	if err != nil {
		return nil, fmt.Errorf("error in OpenWallet:%s", err)
	}

	//log.Infof("wallet info: %v", *wallresp)
	log.Infof("Wallet open success.")

	signer, err := wallet.GetDefaultAccount([]byte(wallresp.Passwd))
	if err != nil {
		return nil, fmt.Errorf("error in GetDefaultAccount:%s", err)
	}

	err = ioutil.WriteFile(prefixRunDir+"txtpswd.txt", []byte(wallresp.Passwd), 0644)
	if err != nil {
		return nil, fmt.Errorf("WriteFile %s error: %s", "txtpswd.txt", err)
	}

	configServer.Signer = signer
	configServer.ServerConfig.SignerAddress = signer.Address.ToBase58()
	log.Infof("config run start %v", configServer.ServerConfig)

	return &configServer, nil
}

var (
	runPath      = flag.String("runPath", "./data/", "runPath flag")
	configPath   = flag.String("configPath", "./appconfig/", "configPath flag")
	contractPath = flag.String("contractPath", "./wasm/", "contractPath flag")
)

func (self *ConfigServer) SendInitTx() error {
	checkcount := uint32(0)
	for {
		_, err := self.OntSdk.SendTransaction(self.InitTx)
		if err != nil {
			if checkcount < 1000 {
				fmt.Printf("SendTransaction init failed %s. try again.", err)
				checkcount += 1
				time.Sleep(3 * time.Second)
				continue
			}
			return fmt.Errorf("SendTransaction init failed %s", err)
		}
		self.OntSdk.WaitForGenerateBlock(30 * time.Second)
		break
	}

	return nil
}

func (self *ConfigServer) UpdateConfigRun(configRunFile string) error {
	DefConfig := self.ServerConfig
	if DefConfig.ServerPort == 0 || DefConfig.CacheTime == 0 || len(DefConfig.Walletname) == 0 || len(DefConfig.SignerAddress) == 0 || len(DefConfig.OntNode) == 0 || len(DefConfig.ContracthexAddr) == 0 || len(DefConfig.Authorize) == 0 || DefConfig.BatchNum == 0 || DefConfig.SendTxInterval == 0 || DefConfig.TryChainInterval == 0 || DefConfig.SendTxSize == 0 {
		return fmt.Errorf("serverconfig not set ok\n")
	}

	okconfig, err := json.Marshal(DefConfig)
	if err != nil {
		return fmt.Errorf("serverconfig Marshal err: %s", err)
	}

	err = ioutil.WriteFile(configRunFile, okconfig, 0644)
	if err != nil {
		return fmt.Errorf("WriteFile %s error: %s", configRunFile, err)
	}

	log.Infof("Success configRun path : %s\n %s\n", configRunFile, string(okconfig))
	return nil
}

func (self *ConfigServer) UpdateStateAddress(state uint32, hexaddress string) error {
	self.State = state
	sink := common.NewZeroCopySink(nil)
	sink.WriteUint32(self.State)
	sink.WriteString(hexaddress)
	return self.DB.Put(GetOtherKeyByHash(KEY_SERVER_STATE), sink.Bytes())
}

func (self *ConfigServer) constructInitTransation(contracthexAddr string) (*types.MutableTransaction, error) {
	self.ServerConfig.ContracthexAddr = contracthexAddr
	owner, err := common.AddressFromBase58(self.ServerConfig.SignerAddress)
	if err != nil {
		return nil, err
	}
	contractAddress, err := common.AddressFromHexString(contracthexAddr)
	if err != nil {
		return nil, err
	}

	gasPrice := self.ServerConfig.GasPrice

	args := make([]interface{}, 2)
	args[0] = "set_owner"
	args[1] = owner

	tx, err := getTxWithArgs(self.OntSdk, args, gasPrice, contractAddress, self.Signer)
	if err != nil {
		return nil, err
	}

	self.InitTx = tx
	return tx, nil
}

func (self *ConfigServer) constructverifytx(contracthexAddr string) (*types.MutableTransaction, error) {
	self.ServerConfig.ContracthexAddr = contracthexAddr
	//owner, err := common.AddressFromBase58(self.ServerConfig.SignerAddress)
	//if err != nil {
	//	return nil, err
	//}
	contractAddress, err := common.AddressFromHexString(contracthexAddr)
	if err != nil {
		return nil, err
	}

	gasPrice := self.ServerConfig.GasPrice

	args := make([]interface{}, 1)
	args[0] = "get_root"

	tx, err := getTxWithArgs(self.OntSdk, args, gasPrice, contractAddress, self.Signer)
	if err != nil {
		return nil, err
	}

	self.VerifyTx = tx

	return tx, nil
}

func (self *ConfigServer) DeployNewContract(wasmfile string) (string, error) {
	codeHash, contracthexAddr, err := GetContractStringAndAddressByfile(wasmfile)
	if err != nil {
		return "", err
	}

	if checkContractExist(self.OntSdk, contracthexAddr, 3) {
		return "", fmt.Errorf("contracthexAddr %s already exist. change another Owner", contracthexAddr)
	}

	deploygaslimit := uint64(200000000)
	_, err = self.OntSdk.WasmVM.DeployWasmVMSmartContract(
		self.ServerConfig.GasPrice,
		deploygaslimit,
		self.Signer,
		codeHash,
		"witness contract",
		"1.0",
		"author",
		"email",
		"desc",
	)

	if err != nil {
		return "", fmt.Errorf("error in DeployWasmVMSmartContract:%s", err)
	}

	self.OntSdk.WaitForGenerateBlock(500 * time.Second)

	if !checkContractExist(self.OntSdk, contracthexAddr, 100) {
		return "", fmt.Errorf("contracthexAddr %s not exist", contracthexAddr)
	}
	fmt.Printf("DeployNewContract: %s success.", contracthexAddr)

	return contracthexAddr, nil
}

func getTxWithArgs(ontSdk *sdk.OntologySdk, args []interface{}, gasPrice uint64, contractAddress common.Address, signer *sdk.Account) (*types.MutableTransaction, error) {
	tx, err := utils2.NewWasmVMInvokeTransaction(gasPrice, 8000000, contractAddress, args)
	if err != nil {
		return nil, fmt.Errorf("create tx failed: %s", err)
	}
	err = ontSdk.SignToTransaction(tx, signer)
	if err != nil {
		return nil, fmt.Errorf("signer tx failed: %s", err)
	}
	return tx, nil
}

func main() {
	flag.Parse()
	log.Infof("runPath : %s\n", *runPath)
	log.Infof("runPath : %s\n", *configPath)
	log.Infof("runPath : %s\n", *contractPath)
	prefixRunDir := *runPath + "/"
	prefixConfigDir := *configPath + "/"
	prefixContractDir := *contractPath + "/"

	configRunFile := prefixRunDir + "config.run.json"
	configFromTenant := prefixConfigDir + "config.json"
	contractname := prefixContractDir + "contract.wasm"
	dbPathName := prefixRunDir + "configleveldb"
	configFixed := "config.fixed.json"

	server, err := NewConfigServer(dbPathName, configFixed, configFromTenant, prefixRunDir)
	if err != nil {
		log.Errorf("NewConfigServer err: %s", err)
		os.Exit(1)
	}
	switch server.State {
	case SERVER_STATE_INIT:
		// deploy contract.
		log.Errorf("impossible get here. due to already deployed")
		contracthexAddr, err := server.DeployNewContract(contractname)
		if err != nil {
			log.Errorf("deploy contract %s failed: %s", contracthexAddr, err)
			os.Exit(1)
		}

		err = server.UpdateStateAddress(SERVER_STATE_DEPLOY_SUCCESS, contracthexAddr)
		if err != nil {
			log.Errorf("sould panic. PutState failed %s", err)
			os.Exit(1)
		}

		// init tx
		_, err = server.constructInitTransation(contracthexAddr)
		if err != nil {
			log.Errorf("NewConfigServer init tx: %s", err)
			os.Exit(1)
		}

		err = server.SendInitTx()
		if err != nil {
			log.Errorf("contract %s init failed", contracthexAddr)
			os.Exit(1)
		}

		err = server.UpdateStateAddress(SERVER_STATE_CONTRACT_INIT, contracthexAddr)
		if err != nil {
			log.Errorf("sould panic. PutState failed %s", err)
			os.Exit(1)
		}

		// update configRunFile.
		err = server.UpdateConfigRun(configRunFile)
		if err != nil {
			log.Errorf("Write %s Failed: %s", configRunFile, err)
			os.Exit(1)
		}
	case SERVER_STATE_DEPLOY_SUCCESS:
		log.Infof("case SERVER_STATE_DEPLOY_SUCCESS")
		if !checkContractExist(server.OntSdk, server.ServerConfig.ContracthexAddr, 3) {
			log.Errorf("SERVER_STATE_DEPLOY_SUCCESS restart contracthexAddr %s not exist", server.ServerConfig.ContracthexAddr)
			os.Exit(1)
		}

		// send init
		_, err = server.constructInitTransation(server.ServerConfig.ContracthexAddr)
		if err != nil {
			log.Errorf("SERVER_STATE_DEPLOY_SUCCES SNewConfigServer init tx: %s", err)
			os.Exit(1)
		}

		err = server.SendInitTx()
		if err != nil {
			log.Errorf("SERVER_STATE_DEPLOY_SUCCESS contract %s init failed", server.ServerConfig.ContracthexAddr)
			os.Exit(1)
		}

		err = server.UpdateStateAddress(SERVER_STATE_CONTRACT_INIT, server.ServerConfig.ContracthexAddr)
		if err != nil {
			log.Errorf("SERVER_STATE_DEPLOY_SUCCESS sould panic. PutState failed %s", err)
			os.Exit(1)
		}

		err = server.UpdateConfigRun(configRunFile)
		if err != nil {
			log.Errorf("SERVER_STATE_DEPLOY_SUCCESS Write %s Failed: %s", configRunFile, err)
		}
	case SERVER_STATE_CONTRACT_INIT:
		err = server.UpdateConfigRun(configRunFile)
		if err != nil {
			log.Errorf("Write %s Failed: %s", configRunFile, err)
		}
	}
}

func GetContractStringAndAddressByfile(wasmfile string) (string, string, error) {
	code, err := ioutil.ReadFile(wasmfile)
	if err != nil {
		return "", "", fmt.Errorf("error in ReadFile:%s", err)
	}

	codeHash := common.ToHexString(code)
	contractAddr, err := utils.GetContractAddress(codeHash)
	if err != nil {
		return "", "", fmt.Errorf("GetContractAddress err: %s", err)
	}
	contracthexAddr := contractAddr.ToHexString()
	return codeHash, contracthexAddr, nil
}

func checkContractExist(ontSdk *sdk.OntologySdk, contracthexAddr string, n uint32) bool {
	checkcount := uint32(0)
	for {
		payload, err := ontSdk.GetSmartContract(contracthexAddr)
		if payload == nil || err != nil {
			if checkcount < n {
				fmt.Printf("GetSmartContract: %s\n", err)
				checkcount += 1
				time.Sleep(2 * time.Second)
				continue
			}

			return false
		}
		break
	}

	return true
}
