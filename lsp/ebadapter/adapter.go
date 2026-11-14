// Package ebadapter connects editbuffer.EditBuffer to lsp.Buffer.
//
// It is a separate package so that language/lsp itself stays free of any
// editor dependency (and testable with a plain fake buffer). The adapter is
// stateless: create one wherever convenient, the Manager identifies the
// buffer by Key, which is the *EditBuffer itself.
package ebadapter

import (
	"github.com/ge-editor/editorleaf/editbuffer"
	"github.com/ge-editor/language/lsp"
)

// Buffer adapts an *editbuffer.EditBuffer to lsp.Buffer. All methods must
// be called on the UI goroutine, like the EditBuffer's own.
type Buffer struct {
	EB *editbuffer.EditBuffer
}

// New returns the adapter for eb.
func New(eb *editbuffer.EditBuffer) *Buffer { return &Buffer{EB: eb} }

var _ lsp.Buffer = (*Buffer)(nil)

// Key identifies the buffer by the EditBuffer pointer, so two adapters for
// the same EditBuffer address the same document.
func (b *Buffer) Key() any { return b.EB }

// Path returns the buffer's absolute path ("" for buffers without one,
// such as the minibuffer).
func (b *Buffer) Path() string { return b.EB.GetPath() }

// Snapshot copies every row and returns it with eb.Version() of that
// content.
func (b *Buffer) Snapshot() ([][]byte, uint64) {
	var rows [][]byte
	if rs := b.EB.Rows(); rs != nil {
		rows = make([][]byte, len(*rs))
		for i, r := range *rs {
			rows[i] = append([]byte(nil), r...)
		}
	}
	return rows, b.EB.Version()
}

// AddChangeListener registers fn for every modification and converts
// editbuffer.Change into lsp.Change. Change.Text of editbuffer is already
// an independent copy, so it is passed on without another copy.
func (b *Buffer) AddChangeListener(fn func(lsp.Change)) (remove func()) {
	return b.EB.AddChangeListener(func(c editbuffer.Change) {
		fn(convert(c))
	})
}

// Version returns eb.Version().
func (b *Buffer) Version() uint64 { return b.EB.Version() }

// AddSaveListener registers fn for every successful save of the buffer.
func (b *Buffer) AddSaveListener(fn func()) (remove func()) {
	return b.EB.AddSaveListener(editbuffer.SaveListener(fn))
}

func convert(c editbuffer.Change) lsp.Change {
	out := lsp.Change{
		Version: c.Version,
		Start:   lsp.Pos{Row: c.Start.RowIndex, Col: c.Start.ColIndex},
		OldEnd:  lsp.Pos{Row: c.OldEnd.RowIndex, Col: c.OldEnd.ColIndex},
	}

	switch c.Kind {
	case editbuffer.ChangeInsert:
		out.Kind = lsp.ChangeInsert
	case editbuffer.ChangeDelete:
		out.Kind = lsp.ChangeDelete
	default:
		out.Kind = lsp.ChangeReset
	}

	if len(c.Text) > 0 {
		out.Text = make([][]byte, len(c.Text))
		for i, r := range c.Text {
			out.Text[i] = []byte(r)
		}
	}
	return out
}
