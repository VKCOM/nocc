package server

import "github.com/VKCOM/nocc/internal/common"

// anywhere in the server code, use logServer.Info() and other methods for logging
var logServer *common.LoggerWrapper

func MakeLoggerServer(logFile string, verbosity int64) error {
	var err error
	logServer, err = common.MakeLogger(logFile, verbosity, false, false)
	return err
}
