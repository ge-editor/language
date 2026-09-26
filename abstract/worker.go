package abstract

import (
	"log"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ge-editor/gecore/lang"
)

// --------------------
// AbstractWorker implement gecore Mode interface
// --------------------

func NewWorker() lang.Mode {
	return &Worker{
		exts: []string{},
	}
}

type Worker struct {
	exts      []string
	tabWidth  int
	isSoftTab bool
}

func (aw *Worker) Name() string {
	// Get type name by reflection.
	typeName := reflect.TypeOf(aw).Elem().Name()
	log.Fatalf("implements 'Name' method on %s\n", typeName)
	return typeName
}

// Matches checks if the given file path matches any of the extensions in GoMode.exts
func (aw *Worker) HasMatchingExtension(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath)) // Get the file extension in lowercase
	for _, validExt := range aw.exts {
		if ext == strings.ToLower(validExt) { // Case-insensitive comparison
			return true
		}
	}
	return false
}

// Format formats source code and restores the cursor position.
// The cursor position based on byte indices
func (aw *Worker) Formatting(source []byte /* , cursorRow, cursorCol int */) ([]byte /*  int, int, */, error) {
	return source /* cursorRow, cursorCol, */, nil
}

func (aw *Worker) IsFormattingBeforeSave() bool {
	return false
}

func (aw *Worker) GetDefaultTabWidth() int {
	return 4
}
func (aw *Worker) GetTabWidth() int {
	if aw.tabWidth == 0 {
		aw.tabWidth = aw.GetDefaultTabWidth()
	}
	return aw.tabWidth
}
func (aw *Worker) SetTabWidth(tabWidth int) {
	aw.tabWidth = tabWidth
}

func (aw *Worker) GetDefaultSoftTab() bool {
	return false // Hard TAB
}
func (aw *Worker) GetSoftTab() bool {
	return aw.isSoftTab
}
func (aw *Worker) SetSoftTab(isSoftTab bool) {
	aw.isSoftTab = isSoftTab
}

func (aw *Worker) RecommendedColumnWidth() int {
	return 80
}
