package mode

import (
	"go/format"
	"path/filepath"
	"strings"

	"github.com/ge-editor/gecore/lang"
)

// --------------------
// MarkdownMode implement gecore Mode interface
// --------------------

func NewMarkdownMode() lang.Mode {
	return &MarkdownMode{
		exts:      []string{".md"},
		tabWidth:  4,
		isSoftTab: false,
	}
}

type MarkdownMode struct {
	exts      []string
	tabWidth  int
	isSoftTab bool
}

func (gm *MarkdownMode) Name() string {
	return "Markdown"
}

// Matches checks if the given file path matches any of the extensions in MarkdownMode.exts
func (gm *MarkdownMode) HasMatchingExtension(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath)) // Get the file extension in lowercase
	for _, validExt := range gm.exts {
		if ext == strings.ToLower(validExt) { // Case-insensitive comparison
			return true
		}
	}
	return false
}

// Formats source code
func (gm *MarkdownMode) Formatting(source []byte) ([]byte, error) {
	return format.Source(source)
}

func (gm *MarkdownMode) IsFormattingBeforeSave() bool {
	return true
}

func (gm *MarkdownMode) GetDefaultTabWidth() int {
	return 4
}
func (gm *MarkdownMode) GetTabWidth() int {
	return gm.tabWidth
}
func (gm *MarkdownMode) SetTabWidth(tabWidth int) {
	gm.tabWidth = tabWidth
}

func (gm *MarkdownMode) GetDefaultSoftTab() bool {
	return false // Hard TAB
}
func (gm *MarkdownMode) GetSoftTab() bool {
	return gm.isSoftTab
}
func (gm *MarkdownMode) SetSoftTab(isSoftTab bool) {
	gm.isSoftTab = isSoftTab
}

// RecommendedColumnWidth returns the recommended column width.
// It currently follows the default width used by golines.
func (gm *MarkdownMode) RecommendedColumnWidth() int {
	return 80
}

func (gm *MarkdownMode) PageLineCount() int {
	return 60
}
