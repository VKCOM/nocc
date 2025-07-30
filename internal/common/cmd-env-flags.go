// This module provides integration of the flag package with environment variables.
// The purpose to launch either `nocc-server -log-filename fn.log` or `NOCC_LOG_FILENAME=fn.log nocc-server`.
// See usages of CmdEnvString and others.

package common

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type cmdLineArg interface {
	flag.Value
	isFlagSet() bool
	getCmdName() string
	getEnvName() string
	getDescription() string
}

var allCmdLineArgs []cmdLineArg

type cmdLineArgString struct {
	cmdName string
	envName string
	usage   string

	isSet        bool
	defaultValue string
	value        string
}

func (s *cmdLineArgString) String() string {
	return s.value
}

func (s *cmdLineArgString) Set(v string) error {
	s.isSet = true
	s.value = v
	return nil
}

func (s *cmdLineArgString) getCmdName() string {
	return s.cmdName
}

func (s *cmdLineArgString) getEnvName() string {
	return s.envName
}

func (s *cmdLineArgString) getDescription() string {
	return s.usage
}

func (s *cmdLineArgString) isFlagSet() bool {
	return s.isSet
}

type cmdLineArgBool struct {
	cmdName string
	envName string
	usage   string

	isSet        bool
	defaultValue bool
	value        bool
}

func (s *cmdLineArgBool) String() string {
	return strconv.FormatBool(s.value)
}

func (s *cmdLineArgBool) Set(v string) error {
	s.isSet = true
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	s.value = b
	return nil
}

func (s *cmdLineArgBool) IsBoolFlag() bool {
	return true
}

func (s *cmdLineArgBool) getCmdName() string {
	return s.cmdName
}

func (s *cmdLineArgBool) getEnvName() string {
	return s.envName
}

func (s *cmdLineArgBool) getDescription() string {
	return s.usage
}

func (s *cmdLineArgBool) isFlagSet() bool {
	return s.isSet
}

type cmdLineArgInt struct {
	cmdName string
	envName string
	usage   string

	isSet        bool
	defaultValue int64
	value        int64
}

func (s *cmdLineArgInt) String() string {
	return strconv.FormatInt(s.value, 10)
}

func (s *cmdLineArgInt) Set(v string) error {
	s.isSet = true
	b, err := strconv.ParseInt(v, 10, 0)
	if err != nil {
		return err
	}
	s.value = b
	return nil
}

func (s *cmdLineArgInt) getCmdName() string {
	return s.cmdName
}

func (s *cmdLineArgInt) getEnvName() string {
	return s.envName
}

func (s *cmdLineArgInt) getDescription() string {
	return s.usage
}

func (s *cmdLineArgInt) isFlagSet() bool {
	return s.isSet
}

type cmdLineArgDuration struct {
	cmdName string
	envName string
	usage   string

	isSet        bool
	defaultValue time.Duration
	value        time.Duration
}

func (s *cmdLineArgDuration) String() string {
	return s.value.String()
}

func (s *cmdLineArgDuration) Set(v string) error {
	s.isSet = true
	t, err := time.ParseDuration(v)
	if err != nil {
		return err
	}
	s.value = t
	return nil
}

func (s *cmdLineArgDuration) getCmdName() string {
	return s.cmdName
}

func (s *cmdLineArgDuration) getEnvName() string {
	return s.envName
}

func (s *cmdLineArgDuration) getDescription() string {
	return s.usage
}

func (s *cmdLineArgDuration) isFlagSet() bool {
	return s.isSet
}

func initCmdFlag(s cmdLineArg, cmdName string, usage string) {
	if cmdName != "" { // only env var makes sense
		flag.Var(s, cmdName, usage)
	}
}

func customPrintUsage() {
	fmt.Printf("Usage of %s:\n\n", os.Args[0])
	for _, f := range allCmdLineArgs {
		if f.getCmdName() == "v" { // don't print "-v" (shortcut for -version)
			continue
		}

		valueHint := ""
		if _, is := f.(*cmdLineArgString); is {
			valueHint = " string"
		}
		if _, is := f.(*cmdLineArgInt); is {
			valueHint = " integer"
		}
		if _, is := f.(*cmdLineArgDuration); is {
			valueHint = " duration"
		}
		if f.getCmdName() == "version" {
			valueHint = " / -v"
		}
		if f.getCmdName() != "" {
			fmt.Printf("  -%s%s\n", f.getCmdName(), valueHint)
		}
		if f.getEnvName() != "" {
			fmt.Printf("  %s=\n", f.getEnvName())
		}
		fmt.Print("    \t")
		fmt.Print(strings.ReplaceAll(f.getDescription(), "\n", "\n    \t"))
		fmt.Print("\n\n")
	}
}

func CmdEnvString(usage string, defaultValue string, cmdFlagName string, envName string) *string {
	sf := &cmdLineArgString{cmdFlagName, envName, usage, false, defaultValue, defaultValue}
	allCmdLineArgs = append(allCmdLineArgs, sf)
	initCmdFlag(sf, cmdFlagName, usage)
	return &sf.value
}

func CmdEnvBool(usage string, defaultValue bool, cmdFlagName string, envName string) *bool {
	var sf = &cmdLineArgBool{cmdFlagName, envName, usage, false, defaultValue, defaultValue}
	allCmdLineArgs = append(allCmdLineArgs, sf)
	initCmdFlag(sf, cmdFlagName, usage)
	return &sf.value
}

func CmdEnvInt(usage string, defaultValue int64, cmdFlagName string, envName string) *int64 {
	var sf = &cmdLineArgInt{cmdFlagName, envName, usage, false, defaultValue, defaultValue}
	allCmdLineArgs = append(allCmdLineArgs, sf)
	initCmdFlag(sf, cmdFlagName, usage)
	return &sf.value
}

func CmdEnvDuration(usage string, defaultValue time.Duration, cmdFlagName string, envName string) *time.Duration {
	var sf = &cmdLineArgDuration{cmdFlagName, envName, usage, false, defaultValue, defaultValue}
	allCmdLineArgs = append(allCmdLineArgs, sf)
	initCmdFlag(sf, cmdFlagName, usage)
	return &sf.value
}

func ParseCmdFlagsCombiningWithEnv() {
	flag.Usage = customPrintUsage
	flag.Parse()
	for _, f := range allCmdLineArgs {
		// override by a corresponding ENV_NAME if a command-line --flag not provided
		if !f.isFlagSet() && f.getEnvName() != "" {
			if envVal := os.Getenv(f.getEnvName()); envVal != "" {
				if err := f.Set(envVal); err != nil {
					fmt.Printf("error parsing %s env var: %v", f.getEnvName(), err)
					flag.Usage()
					os.Exit(2)
				}
			}
		}
	}
}
