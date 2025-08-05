package client

import "testing"

func TestOther(t *testing.T) {
	tests := []struct {
		optionName string
		flag       string
		isPrefix   bool
	}{
		{
			optionName: "-option",
			flag:       "-option=args",
			isPrefix:   true,
		},
		{
			optionName: "--option",
			flag:       "--option=args",
			isPrefix:   true,
		},
		{
			optionName: "-option",
			flag:       "-option",
			isPrefix:   true,
		},
		{
			optionName: "--option",
			flag:       "--option",
			isPrefix:   true,
		},

		// Negative
		{
			optionName: "options",
			flag:       "-options",
			isPrefix:   false,
		},
		{
			optionName: "-options",
			flag:       "--options",
			isPrefix:   false,
		},
		{
			optionName: "--options",
			flag:       "-options",
			isPrefix:   false,
		},
		{
			optionName: "-option",
			flag:       "-options=args",
			isPrefix:   false,
		},
	}

	for _, tt := range tests {
		isPrefix := HasPrefixOrEqualOption(tt.optionName, tt.flag)
		if isPrefix != tt.isPrefix {
			t.Errorf("want: '%t', got: '%t' (optionName: '%s', flag: '%s')", tt.isPrefix, isPrefix, tt.optionName, tt.flag)
		}
	}
}
