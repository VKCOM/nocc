# for local testing, assuming that bin/nocc-server is running locally

NOCC_BIN_FOLDER=$(pwd)/../../../bin
export NOCC_GO_EXECUTABLE=$NOCC_BIN_FOLDER/nocc-daemon
export NOCC_LOG_VERBOSITY=1
export NOCC_LOG_FILENAME=logg.txt
export NOCC_SERVERS=127.0.0.1:43210

rm -rf build
mkdir build
cd build
if [ -f "/usr/bin/clang++" ]; then
  cmake -DCMAKE_CXX_COMPILER_LAUNCHER=$NOCC_BIN_FOLDER/nocc -DCMAKE_CXX_COMPILER=/usr/bin/clang++ ..
else
  cmake -DCMAKE_CXX_COMPILER_LAUNCHER=$NOCC_BIN_FOLDER/nocc ..
fi
make -j4
