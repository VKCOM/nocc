package common

import (
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strconv"
)

func MkdirForFile(fileName string) error {
	if err := os.MkdirAll(filepath.Dir(fileName), os.ModePerm); err != nil {
		return err
	}
	return nil
}

func OpenTempFile(fullPath string) (f *os.File, err error) {
	fileNameTmp := fullPath + "." + strconv.Itoa(rand.Int())
	return os.OpenFile(fileNameTmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, os.ModePerm)
}

func ReplaceFileExt(fileName string, newExt string) string {
	logExt := path.Ext(fileName)
	return fileName[0:len(fileName)-len(logExt)] + newExt
}
