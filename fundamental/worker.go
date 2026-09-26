package fundamental

import (
	"github.com/ge-editor/gecore/lang"
	"github.com/ge-editor/language/abstract"
)

// --------------------
// Fundamental implement gecore Mode interface
// --------------------

func NewFundamental() lang.Mode {
	return &Fundamental{
		Worker: abstract.Worker{},
		exts:   []string{},
	}
}

type Fundamental struct {
	abstract.Worker
	exts      []string
	tabWidth  int
	isSoftTab bool
}

func (f *Fundamental) Name() string {
	return "Fundamental"
}
