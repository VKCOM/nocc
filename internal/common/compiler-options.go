package common

import (
	"strings"
)

var prefixMapArgs = [...]string{
	"-ffile-prefix-map", "-fdebug-prefix-map", "-fcoverage-prefix-map",
	"-fprofile-prefix-map", "-fmacro-prefix-map",
}

// PrefixMapOptionsGroup function is needed for correct processing of corresponding compiler arguments.
// Most likely, if you use flags such as -ffile-prefix-map, -fdebug-prefix-map, -fcoverage-prefix-map,
// -fprofile-prefix-map and -fmacro-prefix-map to replace the actual directory with another one, but when using nocc,
// it is not so convenient, as if the project was built locally. Therefore, a placeholder template "%WORKING-DIR%" is
// provided for these flags which will be replaced with the client's working folder or ignoring for compiling locally.
// For example: the '-file-prefix-map=%WORKING_DIR%$(pwd)=./' option on the server will be replaced with
// '-file-prefix-map=/tmp/nocc/cpp/clients/{ClientID}$(pwd)=./', but locally with '-file-prefix-map=$(pwd)=./'.
func PrefixMapOptionsGroup(cxxArg string, replaced string) string {
	for j := 0; j < len(prefixMapArgs); j++ {
		if strings.Contains(cxxArg, prefixMapArgs[j]) {
			return strings.ReplaceAll(cxxArg, "%WORKING_DIR%", replaced)
		}
	}

	return cxxArg
}
