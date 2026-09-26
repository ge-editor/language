package go_mode

import (
	"go/format"
	"path/filepath"
	"strings"

	"github.com/ge-editor/gecore/lang"
)

// --------------------
// GoMode implement gecore Mode interface
// --------------------

func NewGoMode() lang.Mode {
	return &GoMode{
		exts:      []string{".go" /* , ".mod", ".sum" */},
		tabWidth:  4,
		isSoftTab: false,
	}
}

type GoMode struct {
	exts      []string
	tabWidth  int
	isSoftTab bool
}

func (gm *GoMode) Name() string {
	return "Go"
}

// Matches checks if the given file path matches any of the extensions in GoMode.exts
func (gm *GoMode) HasMatchingExtension(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath)) // Get the file extension in lowercase
	for _, validExt := range gm.exts {
		if ext == strings.ToLower(validExt) { // Case-insensitive comparison
			return true
		}
	}
	return false
}

// Formats source code
func (gm *GoMode) Formatting(source []byte) ([]byte, error) {
	return format.Source(source)
}

func (gm *GoMode) IsFormattingBeforeSave() bool {
	return true
}

func (gm *GoMode) GetDefaultTabWidth() int {
	return 4
}
func (gm *GoMode) GetTabWidth() int {
	return gm.tabWidth
}
func (gm *GoMode) SetTabWidth(tabWidth int) {
	gm.tabWidth = tabWidth
}

func (gm *GoMode) GetDefaultSoftTab() bool {
	return false // Hard TAB
}
func (gm *GoMode) GetSoftTab() bool {
	return gm.isSoftTab
}
func (gm *GoMode) SetSoftTab(isSoftTab bool) {
	gm.isSoftTab = isSoftTab
}

// RecommendedColumnWidth returns the recommended column width.
// It currently follows the default width used by golines.
func (gm *GoMode) RecommendedColumnWidth() int {
	return 100
}
