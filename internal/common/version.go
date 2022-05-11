package common

// version is provided by `go build`, see Makefile (same for client and server)
var version string

func GetVersion() string {
	if len(version) == 0 {
		return "Unknown"
	}
	return version
}
