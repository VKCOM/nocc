package server

import (
	"path"
	"strings"
)

const prefixMapOption = "-ffile-prefix-map"

// FilePrefixMapOption function is needed for correct processing of corresponding compiler argument. If you use
// the `-file-prefix-map` flag to replace the actual directory with another one when compiling locally, everything
// is fine. However, if you use `nocc`, this will have a different effect because `nocc-server` saves the sources in
// a different location using a specific prefix (for example, `/tmp/nocc/cpp/clients/{ClientID}`). If you specify
// the old path using absolute path (`-ffile-prefix-map=/old/path=new`), a prefix will be added to the specified
// path, where `nocc-server` stores the sources (`-ffile-prefix-map=/tmp/nocc/cpp/clients/{ClientID}/old/path=new`).
func FilePrefixMapOption(cxxArg string, replaced string) string {
	if strings.HasPrefix(cxxArg, prefixMapOption) {
		parts := strings.Split(cxxArg, "=")
		if len(parts) >= 2 && path.IsAbs(parts[1]) {
			parts[1] = path.Join(replaced, parts[1])
			cxxArg = strings.Join(parts, "=")
		}
		return cxxArg
	}
	return cxxArg
}
