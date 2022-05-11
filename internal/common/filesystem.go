package common

import (
	"os"
	"path"
	"path/filepath"
)

func MkdirForFile(fileName string) error {
	if err := os.MkdirAll(filepath.Dir(fileName), os.ModePerm); err != nil {
		return err
	}
	return nil
}

func OpenTempFile(fullPath string, mkdir bool) (f *os.File, err error) {
	directory, fileName := filepath.Split(fullPath)
	if mkdir {
		if err := os.MkdirAll(directory, os.ModePerm); err != nil {
			return nil, err
		}
	}
	return os.CreateTemp(directory, fileName)
}

func ReplaceFileExt(fileName string, newExt string) string {
	logExt := path.Ext(fileName)
	return fileName[0:len(fileName)-len(logExt)] + newExt
}
