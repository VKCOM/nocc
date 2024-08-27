package client

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/pb"
)

// IncludedFile is a dependency for a .cpp compilation (a resolved #include directive, a pch file, a .cpp itself).
// Actually, fileName extension is not .h always: it could be .h/.hpp/.inc/.inl/.nocc-pch/etc.
type IncludedFile struct {
	fileName   string        // full path, starts with /
	fileSize   int64         // size in bytes
	fileSHA256 common.SHA256 // hash of contents; for KPHP, it's //crc from the header; for pch, hash of deps
}

func (file *IncludedFile) ToPbFileMetadata() *pb.FileMetadata {
	return &pb.FileMetadata{
		ClientFileName: file.fileName,
		FileSize:       file.fileSize,
		SHA256_B0_7:    file.fileSHA256.B0_7,
		SHA256_B8_15:   file.fileSHA256.B8_15,
		SHA256_B16_23:  file.fileSHA256.B16_23,
		SHA256_B24_31:  file.fileSHA256.B24_31,
	}
}

// CollectDependentIncludesByCxxM collects all dependencies for an input .cpp file USING `cxx -M`.
// It launches cxx locally — but only the preprocessor, not compilation (since compilation will be done remotely).
// The -M flag of cxx runs the preprocessor and outputs dependencies of the .cpp file.
// We need dependencies to upload them to remote.
// Since cxx knows nothing about .nocc-pch files, it will output all dependencies regardless of -fpch-preprocess flag.
// We'll manually add .nocc-pch if found, so the remote is supposed to use it, not its nested dependencies, actually.
// See https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html
func CollectDependentIncludesByCxxM(includesCache *IncludesCache, cwd string, cxxName string, cppInFile string, cxxArgs []string, cxxIDirs IncludeDirs) (hFiles []*IncludedFile, cppFile IncludedFile, err error) {
	cxxCmdLine := make([]string, 0, len(cxxArgs)+2*cxxIDirs.Count()+4)
	cxxCmdLine = append(cxxCmdLine, cxxArgs...)
	cxxCmdLine = append(cxxCmdLine, cxxIDirs.AsCxxArgs()...)
	cxxCmdLine = append(cxxCmdLine, "-o", "/dev/stdout", "-M", cppInFile)

	// drop "-Xclang -emit-pch", as it outputs pch regardless of -M flag
	for i, arg := range cxxCmdLine {
		if arg == "-Xclang" && i < len(cxxCmdLine)-1 && cxxCmdLine[i+1] == "-emit-pch" {
			cxxCmdLine = append(cxxCmdLine[:i], cxxCmdLine[i+2:]...)
			break
		}
	}

	cxxMCommand := exec.Command(cxxName, cxxCmdLine...)
	cxxMCommand.Dir = cwd
	var cxxMStdout, cxxMStderr bytes.Buffer
	cxxMCommand.Stdout = &cxxMStdout
	cxxMCommand.Stderr = &cxxMStderr
	if err = cxxMCommand.Run(); err != nil {
		if err.(*exec.ExitError) != nil {
			err = fmt.Errorf("%s exited with code %d: %s", cxxName, cxxMCommand.ProcessState.ExitCode(), cxxMStderr.String())
		}
		return
	}

	// -M outputs all dependent file names (we call them ".h files", though the extension is arbitrary).
	// We also need size and sha256 for every dependency: we'll use them to check whether they were already uploaded.
	hFilesNames := extractIncludesFromCxxMStdout(cxxMStdout.Bytes())
	hFiles = make([]*IncludedFile, 0, len(hFilesNames))
	preallocatedBuf := make([]byte, 32*1024)

	fillSizeAndMTime := func(dest *IncludedFile) error {
		file, err := os.Open(dest.fileName)
		if err == nil {
			var stat os.FileInfo
			stat, err = file.Stat()
			if err == nil {
				dest.fileSize = stat.Size()
				dest.fileSHA256, _, err = CalcSHA256OfFile(file, dest.fileSize, preallocatedBuf)
			}
			_ = file.Close()
		}
		return err
	}

	addHFile := func(hFileName string, searchForPch bool) error {
		if searchForPch {
			if pchFile := LocateOwnPchFile(hFileName, includesCache); pchFile != nil {
				hFiles = append(hFiles, pchFile)
				return nil
			}
		}
		hFile := &IncludedFile{fileName: hFileName}
		if err := fillSizeAndMTime(hFile); err != nil {
			return err
		}
		hFiles = append(hFiles, hFile)
		return nil
	}

	// do not parallelize here to fit the system ulimit -n (cause includes collecting is also launched in parallel)
	// it's slow, but enabling non-own include parser is for testing/bugs searching, so let it be
	searchForPch := isSourceFileName(cppInFile)
	for _, hFileName := range hFilesNames {
		err = addHFile(hFileName, searchForPch)
		if err != nil {
			return
		}
	}

	cppFile = IncludedFile{fileName: cppInFile}
	err = fillSizeAndMTime(&cppFile)
	return
}

// GetDefaultCxxIncludeDirsOnLocal retrieves default include dirs on a local machine.
// This is done by -Wp,-v option for a no input file.
// This result is cached once nocc-daemon is started.
func GetDefaultCxxIncludeDirsOnLocal(cxxName string) (IncludeDirs, error) {
	cxxWpCommand := exec.Command(cxxName, "-Wp,-v", "-x", "c++", "/dev/null", "-fsyntax-only")
	var cxxWpStderr bytes.Buffer
	cxxWpCommand.Stderr = &cxxWpStderr
	if err := cxxWpCommand.Run(); err != nil {
		return IncludeDirs{}, err
	}

	return parseCxxDefaultIncludeDirsFromWpStderr(cxxWpStderr.String()), nil
}

// CalcSHA256OfFile reads the opened file up to end and returns its sha256 and contents.
func CalcSHA256OfFile(file *os.File, fileSize int64, preallocatedBuf []byte) (common.SHA256, []byte, error) {
	var buffer []byte
	if fileSize > int64(len(preallocatedBuf)) {
		buffer = make([]byte, fileSize)
	} else {
		buffer = preallocatedBuf[:fileSize]
	}
	_, err := io.ReadFull(file, buffer)
	if err != nil {
		return common.SHA256{}, nil, err
	}

	// optimization for KPHP (it inserts a header into every autogenerated file)
	if len(buffer) > 70 && buffer[0] == '/' && buffer[1] == '/' && buffer[2] == 'c' {
		var headCrc64 uint64 = 0
		var headCrc64WithComments uint64 = 0
		if n, _ := fmt.Fscanf(bytes.NewReader(buffer), "//crc64:%x\n//crc64_with_comments:%x\n", &headCrc64, &headCrc64WithComments); n == 2 {
			return common.SHA256{B0_7: headCrc64, B8_15: headCrc64WithComments}, buffer, nil
		}
	}

	hasher := sha256.New()
	_, _ = hasher.Write(buffer)
	return common.MakeSHA256Struct(hasher), buffer, nil
}

// CalcSHA256OfFileName is like CalcSHA256OfFile but for a file name, not descriptor.
func CalcSHA256OfFileName(fileName string, preallocatedBuf []byte) (common.SHA256, []byte, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return common.SHA256{}, nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return common.SHA256{}, nil, err
	}

	return CalcSHA256OfFile(file, stat.Size(), preallocatedBuf)
}

// LocateOwnPchFile finds a .nocc-pch file next to .h.
// The results are cached: if a file doesn't exist, it won't be looked up again until daemon is alive.
func LocateOwnPchFile(hFileName string, includesCache *IncludesCache) *IncludedFile {
	ownPchFile := hFileName + ".nocc-pch"
	pchCached, exists := includesCache.GetHFileInfo(ownPchFile)
	if !exists {
		if stat, err := os.Stat(ownPchFile); err == nil {
			ownPch, err := common.ParseOwnPchFile(ownPchFile)
			if err == nil {
				includesCache.AddHFileInfo(ownPchFile, stat.Size(), ownPch.PchHash, []string{})
			} else {
				logClient.Error(err)
				includesCache.AddHFileInfo(ownPchFile, -1, common.SHA256{}, []string{})
			}
		} else {
			includesCache.AddHFileInfo(ownPchFile, -1, common.SHA256{}, []string{})
		}
		pchCached, _ = includesCache.GetHFileInfo(ownPchFile)
	}

	if pchCached.fileSize == -1 {
		return nil
	}
	return &IncludedFile{ownPchFile, pchCached.fileSize, pchCached.fileSHA256}
}

// parseCxxDefaultIncludeDirsFromWpStderr parses output of a C++ compiler with -Wp,-v option.
func parseCxxDefaultIncludeDirsFromWpStderr(cxxWpStderr string) IncludeDirs {
	const (
		dirsIStart      = "#include <...>"
		dirsIquoteStart = "#include \"...\""
		dirsEnd         = "End of search list"

		stateUnknown      = 0
		stateInDirsIquote = 1
		stateInDirsI      = 2
	)

	state := stateUnknown
	cxxDefIncludeDirs := MakeIncludeDirs()
	for _, line := range strings.Split(cxxWpStderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, dirsIquoteStart) {
			state = stateInDirsIquote
		} else if strings.HasPrefix(line, dirsIStart) {
			state = stateInDirsI
		} else if strings.HasPrefix(line, dirsEnd) {
			return cxxDefIncludeDirs
		} else if strings.HasPrefix(line, "/") {
			if strings.HasSuffix(line, "(framework directory)") {
				continue
			}
			switch state {
			case stateInDirsIquote:
				cxxDefIncludeDirs.dirsIquote = append(cxxDefIncludeDirs.dirsIquote, line)
			case stateInDirsI:
				if strings.HasPrefix(line, "/usr/") || strings.HasPrefix(line, "/Library/") {
					normalizedPath, err := filepath.Abs(line)
					if err != nil {
						logClient.Error("can't normalize path:", line)
						continue
					}
					cxxDefIncludeDirs.dirsIsystem = append(cxxDefIncludeDirs.dirsIsystem, normalizedPath)
				} else {
					cxxDefIncludeDirs.dirsI = append(cxxDefIncludeDirs.dirsI, line)
				}
			}
		}
	}
	return cxxDefIncludeDirs
}

// extractIncludesFromCxxMStdout parses output of a C++ compiler with -M option (a dependency list for Makefile).
func extractIncludesFromCxxMStdout(cxxMStdout []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(cxxMStdout))
	scanner.Split(bufio.ScanWords)
	hFilesNames := make([]string, 0, 16)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "#pragma" && scanner.Scan() && scanner.Text() == "GCC" && scanner.Scan() && scanner.Text() == "pch_preprocess" && scanner.Scan() {
			pchFileName := strings.Trim(scanner.Text(), "\"")
			pchFileName, _ = filepath.Abs(pchFileName)
			hFilesNames = append(hFilesNames, pchFileName)
			continue
		}

		if line == "\\" || isSourceFileName(line) || strings.HasSuffix(line, ".o") || strings.HasSuffix(line, ".o:") {
			continue
		}
		hFileName, _ := filepath.Abs(line)
		hFilesNames = append(hFilesNames, hFileName)
	}
	return hFilesNames
}

// CompareOwnIncludesParserAndCxxM is for development purposes.
// Perform a full-text search for this method call.
//
//goland:noinspection GoUnusedExportedFunction
func CompareOwnIncludesParserAndCxxM(cppInFile string, ownFoundHFiles []IncludedFile, cxxMFoundHFiles []IncludedFile) bool {
	equal := true
	for _, incCxx := range cxxMFoundHFiles {
		found := false
		for _, incOwn := range ownFoundHFiles {
			if incOwn == incCxx {
				found = true
				break
			}
		}
		if !found {
			equal = false
			logClient.Error("gcc -M found", incCxx, "but not found by own include parser", cppInFile)
		}
	}
	return equal
}
