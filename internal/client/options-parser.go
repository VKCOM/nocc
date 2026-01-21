package client

import (
	"fmt"
	"strings"
)

func HasPrefixOrEqualOption(optionName string, flagValue string) bool {
	if flagValue == optionName || strings.HasPrefix(flagValue, fmt.Sprintf("%s=", optionName)) {
		return true
	}

	return false
}
