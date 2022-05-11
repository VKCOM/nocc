package client

import "github.com/VKCOM/nocc/internal/common"

// anywhere in the client code, use logClient.Info() and other methods for logging
var logClient *common.LoggerWrapper

func MakeLoggerClient(logFile string, verbosity int64, noLogsIfEmpty bool) error {
	var err error
	logClient, err = common.MakeLogger(logFile, verbosity, noLogsIfEmpty, true)
	return err
}
