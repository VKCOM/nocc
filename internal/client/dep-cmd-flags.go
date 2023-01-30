package client

import (
	"os"
	"path"
	"strings"

	"github.com/VKCOM/nocc/internal/common"
)

// DepCmdFlags contains flags from the command line to generate .o.d file.
// CMake and make sometimes invoke the compiler like
// > g++ -MD -MT example.dir/1.cpp.o -MF example.dir/1.cpp.o.d -o example.dir/1.cpp.o -c 1.cpp
// This means: along with an object file (1.cpp.o), generate a dependency file (named 1.cpp.o.d here).
// A dependency file is a text file with include list found at any depth.
// Probably, it's used by CMake to track recompilation tree on that files change.
//
// nocc detects options like -MD and emits a depfile on a client side, after having collected all includes.
// Moreover, these options are stripped off invocation.cxxArgs and are not sent to the remote at all.
//
// Some options are supported and handled (-MF {file} / -MT {target} / ...).
// Some are unsupported (-M / -MG / ....). When they occur, nocc falls back to local compilation.
// See https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html.
type DepCmdFlags struct {
	flagMF  string // -MF {abs filename} (pre-resolved at cwd)
	flagMT  string // -MT/-MQ (target name)
	flagMD  bool   // -MD (like -MF {def file})
	flagMMD bool   // -MMD (mention only user header files, not system header files)
	flagMP  bool   // -MP (add a phony target for each dependency other than the main file)

	origO   string // if -MT not set, -o used as a target name (not resolved objOutFile, but as-is from cmdLine)
	origCpp string // a first dependency is an input cpp file, but again, as-is, not resolved cppInFile
}

func (deps *DepCmdFlags) SetCmdFlagMF(absFilename string) {
	deps.flagMF = absFilename
}

func (deps *DepCmdFlags) SetCmdFlagMT(mtTarget string) {
	if len(deps.flagMT) > 0 {
		deps.flagMT += " \\\n "
	}
	deps.flagMT += mtTarget
}

func (deps *DepCmdFlags) SetCmdFlagMQ(mqTarget string) {
	if len(deps.flagMT) > 0 {
		deps.flagMT += " \\\n "
	}
	deps.flagMT += quoteMakefileTarget(mqTarget)
}

func (deps *DepCmdFlags) SetCmdFlagMD() {
	deps.flagMD = true
}

func (deps *DepCmdFlags) SetCmdFlagMMD() {
	deps.flagMMD = true
}

func (deps *DepCmdFlags) SetCmdFlagMP() {
	deps.flagMP = true
}

func (deps *DepCmdFlags) SetCmdOutputFile(origO string) {
	deps.origO = origO
}

func (deps *DepCmdFlags) SetCmdInputFile(origCpp string) {
	deps.origCpp = origCpp
}

// ShouldGenerateDepFile determines whether to output .o.d file besides .o compilation
func (deps *DepCmdFlags) ShouldGenerateDepFile() bool {
	return deps.flagMD || deps.flagMF != ""
}

// GenerateAndSaveDepFile is called if a .o.d file generation is needed.
// Prior to this, all dependencies (hFiles) are already known (via own includes or cxx -M).
// So, here we need only to satisfy depfile format rules.
func (deps *DepCmdFlags) GenerateAndSaveDepFile(invocation *Invocation, hFiles []*IncludedFile) (string, error) {
	targetName := deps.flagMT
	if len(targetName) == 0 {
		targetName = deps.calcDefaultTargetName()
	}

	depFileName := deps.calcOutputDepFileName(invocation)
	depListMainTarget := deps.calcDepListFromHFiles(invocation, hFiles)
	depTargets := []DepFileTarget{
		{targetName, depListMainTarget},
	}

	if deps.flagMP {
		// > This option instructs CPP to add a phony target for each dependency other than the main file,
		// > causing each to depend on nothing.
		for idx, depStr := range depListMainTarget {
			if idx > 0 { // 0 is origCpp
				depTargets = append(depTargets, DepFileTarget{escapeMakefileSpaces(depStr), nil})
			}
		}
	}

	depFile := DepFile{
		DTargets: depTargets,
	}

	return depFileName, depFile.WriteToFile(depFileName)
}

// calcDefaultTargetName returns targetName if no -MT and similar options passed
func (deps *DepCmdFlags) calcDefaultTargetName() string {
	// g++ documentation doesn't satisfy its actual behavior, the implementation seems to be just
	return deps.origO
}

// calcOutputDepFileName returns a name of generated .o.d file based on cmd flags
func (deps *DepCmdFlags) calcOutputDepFileName(invocation *Invocation) string {
	// the -MF option determines the file name
	if deps.flagMF != "" {
		return deps.flagMF
	}

	// without -MF, a file name is constructed in such a way: (a quote from the gcc documentation)
	// > The driver determines file based on whether an -o option is given.
	// > If it is, the driver uses its argument but with a suffix of .d,
	// > otherwise ... (it's not applicable to nocc, as it requires -o anyway)
	if invocation.objOutFile != "" {
		return common.ReplaceFileExt(invocation.objOutFile, ".d")
	}
	return common.ReplaceFileExt(path.Base(invocation.cppInFile), ".d")
}

// calcDepListFromHFiles fills DepFileTarget.TargetDepList
func (deps *DepCmdFlags) calcDepListFromHFiles(invocation *Invocation, hFiles []*IncludedFile) []string {
	if deps.flagMMD {
		hFiles = deps.filterOutSystemHFiles(invocation.includesCache.cxxDefIDirs, hFiles)
	}

	processPwd, _ := os.Getwd()
	if !strings.HasSuffix(processPwd, "/") {
		processPwd += "/"
	}
	relFileName := func(fileName string) string {
		return quoteMakefileTarget(strings.TrimPrefix(fileName, processPwd))
	}

	depList := make([]string, 0, 1+len(hFiles))
	depList = append(depList, quoteMakefileTarget(deps.origCpp))
	for _, hFile := range hFiles {
		depList = append(depList, relFileName(hFile.fileName))
	}

	return depList
}

func (deps *DepCmdFlags) filterOutSystemHFiles(cxxDefIDirs IncludeDirs, hFiles []*IncludedFile) []*IncludedFile {
	userHFiles := make([]*IncludedFile, 0)

	for _, hFile := range hFiles {
		isSystem := false
		for _, sysDir := range cxxDefIDirs.dirsIsystem {
			if strings.HasPrefix(hFile.fileName, sysDir) {
				isSystem = true
			}
		}
		if !isSystem {
			userHFiles = append(userHFiles, hFile)
		}
	}
	return userHFiles
}

// quoteMakefileTarget escapes any characters which are special to Make
func quoteMakefileTarget(targetName string) (escaped string) {
	for i := 0; i < len(targetName); i++ {
		switch targetName[i] {
		case ' ':
		case '\t':
			for j := i - 1; j >= 0 && targetName[j] == '\\'; j-- {
				escaped += string('\\') // escape the preceding backslashes
			}
			escaped += string('\\') // escape the space/tab
		case '$':
			escaped += string('$')
		case '#':
			escaped += string('\\')
		}
		escaped += string(targetName[i])
	}
	return
}
