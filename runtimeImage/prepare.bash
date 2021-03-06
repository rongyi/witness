#!/usr/bin/env bash
#build first
preparedir="./images/"
mkdir -p $preparedir
cd runtimeconfig/
go build confighandle.go aksk.go req.go
cd ..
cd witness_server; go build witness_server.go rpc.go; mv witness_server witness_server_daemon;cd -

cp runtimeconfig/confighandle $preparedir
cp witness_server/witness_server_daemon $preparedir

cp config.fixed.json $preparedir
cp run_server.bash $preparedir
#cp wallet.dat $preparedir
cp runtimeconfig/config.json $preparedir/

## for test
#cd $preparedir
#mkdir appconfig wasm data
#cp ../contract.wasm wasm/
#cp ../config.json appconfig/

# copy web file
cp -r witness_server/css/ $preparedir
cp -r witness_server/fonts/ $preparedir
cp -r witness_server/img/ $preparedir
cp -r witness_server/index.html $preparedir
cp -r witness_server/js/ $preparedir
