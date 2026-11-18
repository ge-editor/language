//go:build cgo

package spell

/*
#cgo pkg-config: hunspell
#include <stdlib.h>
#include <hunspell/hunspell.h>
*/
import "C"

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"
)

// hunspell is a thin wrapper of libhunspell (its C API), the same library
// that LibreOffice and Firefox use. The Hunspell object is not safe for
// concurrent use, so every call takes the mutex.
type hunspell struct {
	mu     sync.Mutex
	handle *C.Hunhandle
}

// openHunspell loads a dictionary. Hunspell_create does not fail for a
// missing file (it gives an empty dictionary), so the files are looked at
// first. Only UTF-8 dictionaries are supported: the words are passed as they
// are.
func openHunspell(affPath, dicPath string) (*hunspell, error) {
	for _, p := range []string{affPath, dicPath} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("dictionary: %w", err)
		}
	}

	cAff := C.CString(affPath)
	defer C.free(unsafe.Pointer(cAff))
	cDic := C.CString(dicPath)
	defer C.free(unsafe.Pointer(cDic))

	handle := C.Hunspell_create(cAff, cDic)
	if handle == nil {
		return nil, fmt.Errorf("dictionary: cannot open %s", dicPath)
	}
	h := &hunspell{handle: handle}

	if enc := C.GoString(C.Hunspell_get_dic_encoding(handle)); !strings.EqualFold(enc, "UTF-8") {
		h.close()
		return nil, fmt.Errorf("dictionary %s is %s: only UTF-8 dictionaries are supported", dicPath, enc)
	}
	return h, nil
}

func (h *hunspell) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handle != nil {
		C.Hunspell_destroy(h.handle)
		h.handle = nil
	}
}

// spell reports whether word is spelled correctly.
func (h *hunspell) spell(word string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handle == nil {
		return true // closed: do not flag anything
	}
	cWord := C.CString(word)
	defer C.free(unsafe.Pointer(cWord))
	return C.Hunspell_spell(h.handle, cWord) != 0
}

// suggest returns the corrections of a misspelled word.
func (h *hunspell) suggest(word string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handle == nil {
		return nil
	}
	cWord := C.CString(word)
	defer C.free(unsafe.Pointer(cWord))

	var list **C.char
	n := int(C.Hunspell_suggest(h.handle, &list, cWord))
	if n <= 0 || list == nil {
		return nil
	}
	defer C.Hunspell_free_list(h.handle, &list, C.int(n))

	out := make([]string, n)
	for i, s := range unsafe.Slice(list, n) {
		out[i] = C.GoString(s)
	}
	return out
}

// add makes word correct until the dictionary is closed.
func (h *hunspell) add(word string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handle == nil {
		return
	}
	cWord := C.CString(word)
	defer C.free(unsafe.Pointer(cWord))
	C.Hunspell_add(h.handle, cWord)
}
