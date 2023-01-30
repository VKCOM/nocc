package client

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// DepFileTarget is one target in .o.d file:
// targetName: dep dep dep
// in a text file, deps are separated by spaces or slash+newlines
type DepFileTarget struct {
	TargetName    string
	TargetDepList []string
}

// DepFile represents a .o.d file after being parsed or at the moment of being generated
type DepFile struct {
	DTargets []DepFileTarget
}

func (dFile *DepFile) FindDepListByTargetName(targetName string) []string {
	for _, dTarget := range dFile.DTargets {
		if dTarget.TargetName == targetName {
			return dTarget.TargetDepList
		}
	}
	return nil
}

// MakeDepFileFromBytes parses contents of .o.d file and creates DepFile
func MakeDepFileFromBytes(dFileContents []byte) (*DepFile, error) {
	depFile := &DepFile{make([]DepFileTarget, 0, 1)}
	return depFile, depFile.parseDepFileContents(string(dFileContents))
}

// MakeDepFileFromFile parses .o.d. file and creates DepFile
func MakeDepFileFromFile(dFileName string) (*DepFile, error) {
	buf, err := os.ReadFile(dFileName)
	if err != nil {
		return nil, err
	}
	return MakeDepFileFromBytes(buf)
}

// WriteToBytes outputs a filled dFile as .o.d representation
func (dFile *DepFile) WriteToBytes() []byte {
	b := bytes.Buffer{}

	for _, dTarget := range dFile.DTargets {
		if b.Len() > 0 {
			b.WriteRune('\n')
		}
		fmt.Fprintf(&b, "%s:", dTarget.TargetName) // note that necessary escaping should be pre-done
		if len(dTarget.TargetDepList) > 0 {
			fmt.Fprintf(&b, " %s", escapeMakefileSpaces(dTarget.TargetDepList[0]))
			for _, hDepFileName := range dTarget.TargetDepList[1:] {
				fmt.Fprintf(&b, " \\\n  %s", escapeMakefileSpaces(hDepFileName))
			}
		}
		b.WriteRune('\n')
	}

	return b.Bytes()
}

// WriteToFile outputs a filled dFile as .o.d file
func (dFile *DepFile) WriteToFile(depFileName string) error {
	asBytes := dFile.WriteToBytes()
	return os.WriteFile(depFileName, asBytes, os.ModePerm)
}

// parseSkipSpaces moves offset to the first non-space
func (dFile *DepFile) parseSkipSpaces(c string, startOffset int) (offset int) {
	offset = startOffset
	for offset < len(c) && unicode.IsSpace(rune(c[offset])) {
		offset++
	}
	return
}

// parseTargetName expects "targetName:" (reads until ':')
func (dFile *DepFile) parseTargetName(c string, startOffset int) (targetName string, offset int, err error) {
	offset = startOffset
	targetName = ""
	for offset < len(c) {
		if c[offset] == ':' {
			offset++
			targetName = escapeMakefileSpaces(targetName)
			return
		} else if c[offset] == '\n' {
			break
		} else if c[offset] == '\\' {
			if c[offset+1] != '\n' {
				targetName += c[offset+1 : offset+2]
			}
			offset += 2
		} else if c[offset] == ' ' {
			if !strings.HasSuffix(targetName, " ") {
				targetName += " "
			}
			offset++
		} else {
			targetName += c[offset : offset+1]
			offset++
		}
	}

	return "", offset, fmt.Errorf("':' expected after %s", c[startOffset:offset])
}

// parseNextDepItem reads next item in TargetDepList (until space or newline)
// depItem can contain space inside, it must be slashed, e.g. "dep\ with\ spaces"
// returns empty depItemName if a list ends
func (dFile *DepFile) parseNextDepItem(c string, startOffset int) (depItemName string, offset int, err error) {
	offset = startOffset
	for offset < len(c) {
		if c[offset] == ' ' {
			offset++
		} else if c[offset] == '\\' {
			offset += 2
		} else {
			break
		}
	}
	if offset >= len(c) {
		return "", offset, nil
	}
	if c[offset] == '\n' {
		return "", offset + 1, nil
	}

	depItemName = ""
	for offset < len(c) {
		if c[offset] == ' ' || c[offset] == '\n' {
			break
		} else if c[offset] == '\\' {
			depItemName += c[offset+1 : offset+2]
			offset += 2
		} else {
			depItemName += c[offset : offset+1]
			offset++
		}
	}
	return
}

// parseDepFileContents fills dFile's properties from scratch, parsing c
func (dFile *DepFile) parseDepFileContents(c string) (err error) {
	var targetName string
	var depItemName string
	var offset = 0

	for {
		offset = dFile.parseSkipSpaces(c, offset)
		if offset >= len(c) {
			break
		}
		targetName, offset, err = dFile.parseTargetName(c, offset)
		if err != nil {
			return
		}
		depItems := make([]string, 0)
		for {
			depItemName, offset, err = dFile.parseNextDepItem(c, offset)
			if err != nil {
				return
			}
			if len(depItemName) == 0 {
				break
			}
			depItems = append(depItems, depItemName)
		}
		dFile.DTargets = append(dFile.DTargets, DepFileTarget{targetName, depItems})
	}
	return nil
}

// escapeMakefileSpaces outputs a string which slashed spaces
func escapeMakefileSpaces(depItemName string) string {
	depItemName = strings.ReplaceAll(depItemName, "\n", "\\\n")
	depItemName = strings.ReplaceAll(depItemName, " ", "\\ ")
	depItemName = strings.ReplaceAll(depItemName, ":", "\\:")
	return depItemName
}
