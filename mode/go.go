package mode

import (
	"go/format"
	"path/filepath"
	"strings"

	"github.com/ge-editor/gecore/lang"
	"github.com/ge-editor/language/lsp"
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

// LSPLanguageID and LSPServer implement lsp.LanguageServerProvider.
func (gm *GoMode) LSPLanguageID() string { return "go" }

func (gm *GoMode) LSPServer() lsp.ServerConfig {
	return lsp.ServerConfig{Command: "gopls"}
}

// goDelimiters: where the prose of a Go file is. The character literal is
// there as code, so that '"' does not start a string.
var goDelimiters = &lang.Delimiters{
	LineComments:  []string{"//"},
	BlockComments: []lang.BlockDelimiter{{Open: "/*", Close: "*/"}},
	Strings: []lang.StringDelimiter{
		{Open: `"`, Close: `"`, Escape: '\\'},
		{Open: "`", Close: "`", Multiline: true},
		{Open: `'`, Close: `'`, Escape: '\\', Code: true},
	},
}

// SpellDelimiters implements lang.SpellDelimiterProvider: the spell check
// looks at the comments and strings.
func (gm *GoMode) SpellDelimiters() *lang.Delimiters { return goDelimiters }

// HasMatchingExtension checks if the given file path matches any of the extensions in GoMode.exts
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

func (gm *GoMode) PageLineCount() int {
	return 60
}

// ShowTrailingWhitespace: gofmt removes trailing whitespace, so any that is
// left is a mistake worth showing.
func (gm *GoMode) ShowTrailingWhitespace() bool {
	return true
}
