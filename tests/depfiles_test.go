package tests

// note, how to run this tests:
// 1) at first, start nocc-server available at 127.0.0.1:43210
// 2) then, run `go test` or these tests from IDE

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/VKCOM/nocc/internal/client"
	"github.com/VKCOM/nocc/internal/common"
)

func compareTwoDepfiles(lhs *client.DepFile, rhs *client.DepFile, lhsDesc string, rhsDesc string) []string {
	normalizeDepItem := func(depItem string) string {
		if !strings.Contains(depItem, ".") {
			return depItem
		}
		return strings.TrimPrefix(path.Clean(depItem), "../tests/")
	}

	diff := make([]string, 0)
	for _, rhsTarget := range rhs.DTargets {
		targetName := rhsTarget.TargetName
		rhsDeps := rhsTarget.TargetDepList
		lhsDeps := lhs.FindDepListByTargetName(targetName)
		if lhsDeps == nil {
			diff = append(diff, fmt.Sprintf("Target '%s' exists in %s but not in %s", targetName, rhsDesc, lhsDesc))
			continue
		}

	outer:
		for _, rhsDep := range rhsDeps {
			for _, lhsDep := range lhsDeps {
				if normalizeDepItem(rhsDep) == normalizeDepItem(lhsDep) {
					continue outer
				}
			}
			diff = append(diff, fmt.Sprintf("Item '%s' for target '%s' exists in %s but not in %s", rhsDep, targetName, rhsDesc, lhsDesc))
		}
	}
	return diff
}

func runGccWithNoccAndCompareOutputDepfiles(t *testing.T, cmdLineStr string, dirToClear string, expectedOutDFileName string) {
	for _, pattern := range []string{"*.o", "*.d"} {
		files, _ := filepath.Glob(dirToClear + "/" + pattern)
		for _, relFn := range files {
			_ = syscall.Unlink(relFn)
		}
	}

	exitCode, output, err := runCmdLocallyForTesting(cmdLineStr)
	if err != nil {
		t.Errorf("Error run gcc %v\n%s", err, output)
		return
	}
	if exitCode != 0 {
		t.Errorf("Gcc exitCode %d\n%s", exitCode, output)
		return
	}

	gccDepsOut, err := client.MakeDepFileFromFile(expectedOutDFileName)
	if err != nil {
		t.Errorf("Error parsing %s after g++: %v", expectedOutDFileName, err)
		return
	}
	_ = os.Rename(expectedOutDFileName, common.ReplaceFileExt(expectedOutDFileName, ".gcc.d"))

	exitCode, stdout, stderr, err := createClientAndEmulateDaemonForTesting(cmdLineStr)
	if err != nil {
		t.Errorf("Error initing nocc client %v", err)
		return
	}
	if exitCode != 0 {
		t.Errorf("Nocc client exitCode %d\nstdout %s\nstderr %s", exitCode, stdout, stderr)
		return
	}
	noccDepsOut, err := client.MakeDepFileFromFile(expectedOutDFileName)
	if err != nil {
		t.Errorf("Error parsing %s after nocc: %v", expectedOutDFileName, err)
		return
	}

	errors := compareTwoDepfiles(noccDepsOut, gccDepsOut, expectedOutDFileName, "gcc")
	if len(errors) != 0 {
		t.Errorf("Diff if d contents:\n%s", strings.Join(errors, "\n"))
	}
}

func Test_MDMTMFMP(t *testing.T) {
	var cmdLineStr = "g++ -MD -MT dt/dep1/1.cpp.o -MF dt/dep1/1.cpp.o.d -o dt/dep1/1.cpp.o -c dt/dep1/1.cpp -MP"
	runGccWithNoccAndCompareOutputDepfiles(t, cmdLineStr, "dt/dep1", "dt/dep1/1.cpp.o.d")
}

func Test_MDMTMFMPMMD(t *testing.T) {
	var cmdLineStr = "g++ -MD -MT dt/dep1/1.cpp.o -MF dt/dep1/1.cpp.o.d -o dt/dep1/1.cpp.o -c dt/dep1/1.cpp -MP -MMD"
	runGccWithNoccAndCompareOutputDepfiles(t, cmdLineStr, "dt/dep1", "dt/dep1/1.cpp.o.d")
}

func Test_MDMF(t *testing.T) {
	var cmdLineStr = "g++ -MD -MF dt/dep1/1.cpp.o.d -o../tests/dt/dep1/1111.cpp.o ../tests/./dt/dep1/1.cpp"
	runGccWithNoccAndCompareOutputDepfiles(t, cmdLineStr, "dt/dep1", "dt/dep1/1.cpp.o.d")
}

func Test_MTMTMQMQMMP(t *testing.T) {
	var cmdLineStr = "g++ -MD -MF dt/dep1/out.d -MT func.i -MT $(#func.j) -MQ $(#func.k) -MQ some/long/option/will/be/placed/on/next/line/func.o -o dt/dep1/1111.cpp.o dt/./dep1/1.cpp -MMD"
	runGccWithNoccAndCompareOutputDepfiles(t, cmdLineStr, "dt/dep1", "dt/dep1/out.d")
}

func Test_parseDepFile(t *testing.T) {
	var dFileName = "dt/some_complex_depfile.o.d"
	depFile, err := client.MakeDepFileFromFile(dFileName)
	if err != nil {
		t.Error(err)
		return
	}

	exported := depFile.WriteToBytes()
	imported, err := client.MakeDepFileFromBytes(exported)
	if err != nil {
		t.Error(err)
		return
	}

	diff1 := compareTwoDepfiles(depFile, imported, "orig depfile", "imported from string")
	if len(diff1) > 0 {
		t.Errorf(strings.Join(diff1, "\n"))
	}

	diff2 := compareTwoDepfiles(imported, depFile, "imported from string", "orig depfile")
	if len(diff2) > 0 {
		t.Errorf(strings.Join(diff2, "\n"))
	}
}
