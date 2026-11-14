package ebadapter

import (
	"strings"
	"testing"

	"github.com/ge-editor/editorleaf/editbuffer"
	"github.com/ge-editor/language/lsp"
)

func TestSnapshotIsIndependentCopy(t *testing.T) {
	eb := editbuffer.NewEditBuffer(nil)
	if err := eb.LoadFrom(strings.NewReader("ab\ncd")); err != nil {
		t.Fatal(err)
	}
	b := New(eb)

	rows, version := b.Snapshot()
	if version != 0 || len(rows) != 2 || string(rows[0]) != "ab" || string(rows[1]) != "cd" {
		t.Fatalf("Snapshot = %q, %d", rows, version)
	}

	// Editing the buffer must not change an earlier snapshot.
	eb.InsertRegion(0, 2, editbuffer.Rows_{editbuffer.Row_("X")})
	if string(rows[0]) != "ab" {
		t.Fatalf("snapshot aliases the buffer: %q", rows[0])
	}
}

func TestChangesAreConverted(t *testing.T) {
	eb := editbuffer.NewEditBuffer(nil)
	if err := eb.LoadFrom(strings.NewReader("hello\nworld")); err != nil {
		t.Fatal(err)
	}
	b := New(eb)

	var got []lsp.Change
	remove := b.AddChangeListener(func(c lsp.Change) { got = append(got, c) })
	defer remove()

	eb.InsertRegion(0, 5, editbuffer.Rows_{editbuffer.Row_("!"), editbuffer.Row_("x")})
	eb.RemoveRegion(
		editbuffer.RowsPos{RowIndex: 0, ColIndex: 1},
		editbuffer.RowsPos{RowIndex: 1, ColIndex: 1},
	)
	eb.SetBytesArray([][]byte{[]byte("new")})

	if len(got) != 3 {
		t.Fatalf("got %d changes", len(got))
	}

	ins := got[0]
	if ins.Kind != lsp.ChangeInsert || ins.Version != 1 || ins.Start != (lsp.Pos{Row: 0, Col: 5}) ||
		len(ins.Text) != 2 || string(ins.Text[0]) != "!" || string(ins.Text[1]) != "x" {
		t.Errorf("insert = %+v", ins)
	}

	del := got[1]
	if del.Kind != lsp.ChangeDelete || del.Version != 2 ||
		del.Start != (lsp.Pos{Row: 0, Col: 1}) || del.OldEnd != (lsp.Pos{Row: 1, Col: 1}) {
		t.Errorf("delete = %+v", del)
	}

	reset := got[2]
	if reset.Kind != lsp.ChangeReset || reset.Version != 3 || len(reset.Text) != 1 || string(reset.Text[0]) != "new" {
		t.Errorf("reset = %+v", reset)
	}
}

func TestKeyIsTheEditBuffer(t *testing.T) {
	eb := editbuffer.NewEditBuffer(nil)
	if New(eb).Key() != New(eb).Key() {
		t.Fatal("two adapters for one EditBuffer must have the same Key")
	}
}

func TestSaveListenerIsForwarded(t *testing.T) {
	eb := editbuffer.NewEditBuffer(nil)
	b := New(eb)

	remove := b.AddSaveListener(func() {})
	if remove == nil {
		t.Fatal("AddSaveListener must return the remove function")
	}
	remove()
}
