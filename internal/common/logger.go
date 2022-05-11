package common

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"
)

type LoggerWrapper struct {
	impl              *log.Logger
	fileName          string
	verbosity         int
	duplicateToStderr bool
}

func MakeLogger(logFile string, verbosity int64, noLogsIfEmpty bool, duplicateToStderr bool) (*LoggerWrapper, error) {
	var impl *log.Logger

	if logFile != "" && logFile != "stderr" {
		out, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			return nil, err
		}
		impl = log.New(out, "", 0)
	} else if !noLogsIfEmpty {
		impl = log.New(os.Stderr, "", 0)
	}

	if verbosity < -1 || verbosity > 2 {
		return nil, errors.New("incorrect verbosity passed")
	}

	return &LoggerWrapper{
		impl:              impl,
		fileName:          logFile,
		verbosity:         int(verbosity),
		duplicateToStderr: duplicateToStderr,
	}, nil
}

func formatStr(prefix string, v ...interface{}) string {
	return fmt.Sprintf("%s %s %s", time.Now().Format("2006-01-02 15:04:05"), prefix, fmt.Sprintln(v...))
}

func (logger *LoggerWrapper) Info(verbosity int, v ...interface{}) {
	if logger.verbosity >= verbosity && logger.impl != nil {
		_ = logger.impl.Output(0, formatStr("INFO", v...))
	}
}

func (logger *LoggerWrapper) Error(v ...interface{}) {
	if logger.impl != nil {
		_ = logger.impl.Output(0, formatStr("ERROR", v...))
	}
	if logger.duplicateToStderr {
		_, _ = fmt.Fprint(os.Stderr, formatStr("[nocc]", v...))
	}
}

func (logger *LoggerWrapper) TmpDebug(v ...interface{}) {
	if logger.impl != nil {
		_ = logger.impl.Output(0, formatStr("DEBUG", v...))
	}
}

func (logger *LoggerWrapper) RotateLogFile() error {
	if logger.fileName == "" {
		return nil
	}
	out, err := os.OpenFile(logger.fileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}

	logger.impl = log.New(out, "", 0)
	return nil
}

func (logger *LoggerWrapper) GetFileName() string {
	return logger.fileName
}

func (logger *LoggerWrapper) GetFileSize() int64 {
	if logger.fileName == "" {
		return 0
	}
	stat, err := os.Stat(logger.fileName)
	if err != nil {
		return 0
	}
	return stat.Size()
}
