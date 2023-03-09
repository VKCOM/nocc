package tests

// note, how to run this tests:
// 1) at first, start nocc-server available at 127.0.0.1:43210
// 2) then, run `go test` or these tests from IDE

import (
	"os"
	"testing"
)

func Test_compileMainCpp(t *testing.T) {
	var cmdLineStr = "g++ -include dt/cmake1/src/all-headers.h -c dt/cmake1/src/main.cpp -o /tmp/mm.o -Winvalid-pch -std=gnu++17"
	exitCode, stdout, stderr, err := createClientAndEmulateDaemonForTesting(cmdLineStr)
	if err != nil {
		t.Errorf("Error initing nocc client %s", err)
		return
	}

	if exitCode != 0 {
		t.Errorf("exitCode %d\nstdout %s\nstderr %s", exitCode, stdout, stderr)
		return
	}

	if len(stdout) > 0 {
		t.Logf("stdout %s\n", stdout)
	}
	if len(stderr) > 0 {
		t.Logf("stderr %s\n", stderr)
	}
}

func Test_relativePath(t *testing.T) {
	var cmdLineStr = "g++ -c dt/path.cpp -o dt/path.cpp.o -std=gnu++17"
	exitCode, stdout, stderr, err := createClientAndEmulateDaemonForTesting(cmdLineStr)
	if err != nil {
		t.Errorf("Error initing nocc client %s", err)
		return
	}

	if exitCode != 0 {
		t.Errorf("exitCode %d\nstdout %s\nstderr %s", exitCode, stdout, stderr)
		return
	}

	if len(stdout) > 0 {
		t.Logf("stdout %s\n", stdout)
	}
	if len(stderr) > 0 {
		t.Logf("stderr %s\n", stderr)
	}

	exitCode, stdout, err = runCmdLocallyForTesting("g++ dt/path.cpp.o -o dt/path -std=gnu++17")
	if err != nil || exitCode != 0 {
		t.Errorf("exitCode %d\n%s", exitCode, stdout)
		return
	}

	if len(stdout) > 0 {
		t.Logf("stdout %s\n", stdout)
	}

	exitCode, stdout, err = runCmdLocallyForTesting("./dt/path")
	if err != nil || exitCode != 0 {
		t.Errorf("exitCode %d\n%s", exitCode, stdout)
		return
	}

	if len(stdout) > 0 {
		t.Logf("stdout %s\n", stdout)
	}

	if string(stdout) != "dt/path.cpp\n" {
		t.Errorf("%s", stdout)
	}
}

func Test_compileCMakeProject1(t *testing.T) {
	if err := runDaemonInBackgroundForTesting(); err != nil {
		t.Error(err)
		return
	}

	cwd := "dt/cmake1"
	prevCwd, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Error(err)
		return
	}
	defer func() {
		_ = os.Chdir(prevCwd)
	}()

	err := os.RemoveAll("build")
	if err != nil {
		t.Error(err)
		return
	}

	exitCode, output, err := runCmdLocallyForTesting("bash build.sh")
	if err != nil {
		t.Error(err)
		return
	}
	if exitCode != 0 {
		t.Errorf("exitCode %d\n%s", exitCode, output)
		return
	}

	if len(output) > 0 {
		t.Logf("output %s\n", output)
	}
}
