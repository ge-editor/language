package mode

import (
	"go/format"
	"path/filepath"
	"strings"

	"github.com/ge-editor/gecore/lang"
)

// --------------------
// OrgMode implement gecore Mode interface
// --------------------

func NewOrgMode() lang.Mode {
	return &OrgMode{
		exts:      []string{".org"},
		tabWidth:  4,
		isSoftTab: false,
	}
}

type OrgMode struct {
	exts      []string
	tabWidth  int
	isSoftTab bool
}

func (org *OrgMode) Name() string {
	return "Org"
}

// Matches checks if the given file path matches any of the extensions in OrgMode.exts
func (org *OrgMode) HasMatchingExtension(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath)) // Get the file extension in lowercase
	for _, validExt := range org.exts {
		if ext == strings.ToLower(validExt) { // Case-insensitive comparison
			return true
		}
	}
	return false
}

// Formats source code
func (org *OrgMode) Formatting(source []byte) ([]byte, error) {
	return format.Source(source)
}

func (org *OrgMode) IsFormattingBeforeSave() bool {
	return true
}

func (org *OrgMode) GetDefaultTabWidth() int {
	return 4
}
func (org *OrgMode) GetTabWidth() int {
	return org.tabWidth
}
func (org *OrgMode) SetTabWidth(tabWidth int) {
	org.tabWidth = tabWidth
}

func (org *OrgMode) GetDefaultSoftTab() bool {
	return false // Hard TAB
}
func (org *OrgMode) GetSoftTab() bool {
	return org.isSoftTab
}
func (org *OrgMode) SetSoftTab(isSoftTab bool) {
	org.isSoftTab = isSoftTab
}

// RecommendedColumnWidth returns the recommended column width.
// It currently follows the default width used by golines.
func (org *OrgMode) RecommendedColumnWidth() int {
	return 80
}

func (org *OrgMode) PageLineCount() int {
	return 60
}
