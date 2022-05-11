package client

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/VKCOM/nocc/internal/common"
)

// DepsCmakeOut contains flags from the command line to generate .o.d file.
// CMake (sometimes with make, often with ninja) invokes the compiler like
// > g++ -MD -MT example.dir/1.cpp.o -MF example.dir/1.cpp.o.d -o example.dir/1.cpp.o -c 1.cpp
// This means: along with an object file (1.cpp.o), generate a dependency file (named 1.cpp.o.d here).
// A dependency file is a text file with include list found at any depth.
// Probably, it's used by CMake to track recompilation tree on that files change.
//
// nocc detects options like -MD and emits a depfile on a client side, after having collected all includes.
// Moreover, these options are stripped off invocation.cxxArgs and are not sent to the remote at all.
//
// The following options are supported: -MF {file}, -MT {target}, -MQ {target}, -MD.
// Others (-M/-MMD/etc.) are unsupported. When they occur, nocc falls back to local compilation.
// See https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html.
type DepsCmakeOut struct {
	flagMF string // -MF {abs filename} (pre-resolved at cwd)
	flagMT string // -MT {target} (stored as is)
	flagMQ string // -MQ {target} (stored as is)
	flagMD bool   // -MD
}

func (deps *DepsCmakeOut) ShouldGenenateDepFile() bool {
	return deps.flagMD || deps.flagMF != ""
}

func (deps *DepsCmakeOut) AsCxxArgs() []string {
	cxxDepArgs := make([]string, 0, 3)

	if deps.flagMD {
		cxxDepArgs = append(cxxDepArgs, "-MD")
	}
	if deps.flagMF != "" {
		cxxDepArgs = append(cxxDepArgs, "-MF", deps.flagMF)
	}
	if deps.flagMQ != "" {
		cxxDepArgs = append(cxxDepArgs, "-MQ", deps.flagMQ)
	}
	if deps.flagMT != "" {
		cxxDepArgs = append(cxxDepArgs, "-MT", deps.flagMT)
	}

	return cxxDepArgs
}

// GenerateAndSaveDepFile is called if a .o.d file generation is needed.
// Prior to this, all dependencies (hFiles) are already known (via own includes or cxx -M).
// So, here we need only to satisfy depfile format rules.
func (deps *DepsCmakeOut) GenerateAndSaveDepFile(invocation *Invocation, hFiles []*IncludedFile) (string, error) {
	depFileName := deps.getDepFileName(invocation)
	depFileContents := deps.getDepFileContents(invocation, hFiles)

	return depFileName, os.WriteFile(depFileName, []byte(depFileContents), os.ModePerm)
}

func (deps *DepsCmakeOut) getDepFileName(invocation *Invocation) string {
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

func (deps *DepsCmakeOut) getDepFileContents(invocation *Invocation, hFiles []*IncludedFile) string {
	quoteTargetName := func(targetName string) string {
		// gcc "quotes any characters which are special to Make"
		// not sure how to do this properly
		return strings.ReplaceAll(targetName, "$", "$$")
	}

	processPwd, _ := os.Getwd()
	if !strings.HasSuffix(processPwd, "/") {
		processPwd += "/"
	}
	relFileName := func(fileName string) string {
		return strings.TrimPrefix(fileName, processPwd)
	}

	var targetName string
	if deps.flagMT != "" {
		targetName = deps.flagMT
	} else if deps.flagMQ != "" {
		targetName = quoteTargetName(deps.flagMQ)
	} else {
		targetName = quoteTargetName(common.ReplaceFileExt(path.Base(invocation.cppInFile), ".o"))
	}

	b := strings.Builder{}
	fmt.Fprintf(&b, "%s:", targetName)
	fmt.Fprintf(&b, " \\\n  %s", relFileName(invocation.cppInFile))
	for _, hFile := range hFiles {
		fmt.Fprintf(&b, " \\\n  %s", relFileName(hFile.fileName))
	}
	fmt.Fprintf(&b, "\n")

	return b.String()
}
