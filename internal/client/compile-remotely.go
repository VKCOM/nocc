package client

import (
	"fmt"
	"strings"

	"github.com/VKCOM/nocc/pb"
)

// CompileCppRemotely executes all steps of remote compilation (see comments inside the function).
// On success, it saves the resulting .o file — the same as if compiled locally.
// It's called within a daemon for every Invocation of type invokedForCompilingCpp.
func CompileCppRemotely(daemon *Daemon, cwd string, invocation *Invocation, remote *RemoteConnection) (exitCode int, stdout []byte, stderr []byte, err error) {
	invocation.wgRecv.Add(1)

	// 1. For an input .cpp file, find all dependent .h/.nocc-pch/etc. that are required for compilation
	hFiles, cppFile, err := invocation.CollectDependentIncludes(cwd, daemon.disableOwnIncludes)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to collect depencies: %v", err)
	}
	invocation.summary.nIncludes = len(hFiles)
	invocation.summary.AddTiming("collected_includes")

	// uncomment this to debug if "cxx -M" finds more #include dependencies than own parser
	// cxxMFoundHFiles, _, _ := invocation.CollectDependentIncludes(true)
	// CompareOwnIncludesParserAndCxxM(invocation.cppInFile, hFiles, cxxMFoundHFiles)

	// if cxx is launched with -MD/-MF flags, it generates a .o.d file (a dependency file with include list)
	// we do it on a client side (moreover, they are stripped off cxxArgs and not sent to the remote)
	// note, that .o.d file is generated ALONG WITH .o (like "a side effect of compilation")
	if invocation.depsFlags.ShouldGenerateDepFile() {
		go func() {
			depFileName, err := invocation.depsFlags.GenerateAndSaveDepFile(invocation, hFiles)
			if err == nil {
				logClient.Info(2, "saved depfile to", depFileName)
			} else {
				logClient.Error("error generating depfile:", err)
			}
		}()
	}

	requiredFiles := make([]*pb.FileMetadata, 0, len(hFiles)+1)
	for _, hFile := range hFiles {
		requiredFiles = append(requiredFiles, hFile.ToPbFileMetadata())
	}
	requiredFiles = append(requiredFiles, cppFile.ToPbFileMetadata())

	// 2. Send sha256 of the .cpp and all dependencies to the remote.
	// The remote returns indexes that are missing (needed to be uploaded).
	fileIndexesToUpload, err := remote.StartCompilationSession(invocation, cwd, requiredFiles)
	if err != nil {
		return 0, nil, nil, err
	}

	logClient.Info(1, "remote", remote.remoteHost, "sessionID", invocation.sessionID, "waiting", len(fileIndexesToUpload), "uploads", invocation.cppInFile)
	logClient.Info(2, "checked", len(requiredFiles), "files whether upload is needed or they exist on remote")
	invocation.summary.AddTiming("remote_session")

	// 3. Send all files needed to be uploaded.
	// If all files were recently uploaded or exist in remote cache, this array would be empty.
	err = remote.UploadFilesToRemote(invocation, requiredFiles, fileIndexesToUpload)
	if err != nil {
		return 0, nil, nil, err
	}
	invocation.summary.AddTiming("uploaded_files")

	// 4. After the remote received all required files, it started compiling .cpp to .o.
	// Here we send a request for this .o, it will wait for .o ready, download and save it.
	logClient.Info(2, "wait for a compiled obj", "sessionID", invocation.sessionID)
	exitCode, stdout, stderr, err = remote.WaitForCompiledObj(invocation)
	if err != nil {
		return
	}
	invocation.summary.AddTiming("received_obj")

	// Now, we have a resulting .o file placed in a path determined by -o from command line.
	if exitCode != 0 {
		logClient.Info(0, "remote C++ compiler exited with code", exitCode, "sessionID", invocation.sessionID, invocation.cppInFile, remote.remoteHost)
		logClient.Info(1, "cxxExitCode:", exitCode, "sessionID", invocation.sessionID, "\ncxxStdout:", strings.TrimSpace(string(invocation.cxxStdout)), "\ncxxStderr:", strings.TrimSpace(string(invocation.cxxStderr)))
	} else {
		logClient.Info(2, "saved obj file to", invocation.objOutFile)
	}
	return
}
