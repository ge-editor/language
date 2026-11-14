package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseWorkspaceEdit(t *testing.T) {
	if got, err := parseWorkspaceEdit(nil); err != nil || got != nil {
		t.Fatalf("nil: %v %v", got, err)
	}
	if got, err := parseWorkspaceEdit([]byte(`null`)); err != nil || got != nil {
		t.Fatalf("null: %v %v", got, err)
	}

	// "changes" form, ordered by URI.
	got, err := parseWorkspaceEdit([]byte(`{"changes":{
		"file:///b.go":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"newText":"B"}],
		"file:///a.go":[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":2}},"newText":"A"}]
	}}`))
	if err != nil || len(got) != 2 || got[0].URI != "file:///a.go" || got[1].URI != "file:///b.go" ||
		got[0].Edits[0].NewText != "A" {
		t.Fatalf("changes: %+v, %v", got, err)
	}

	// "documentChanges" form: edits of one file in separate entries are
	// collected, and it wins over "changes".
	got, err = parseWorkspaceEdit([]byte(`{
		"changes":{"file:///ignored.go":[]},
		"documentChanges":[
			{"textDocument":{"uri":"file:///a.go","version":3},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"newText":"1"}]},
			{"textDocument":{"uri":"file:///a.go","version":3},"edits":[{"range":{"start":{"line":2,"character":0},"end":{"line":2,"character":1}},"newText":"2"}]}
		]}`))
	if err != nil || len(got) != 1 || got[0].URI != "file:///a.go" || len(got[0].Edits) != 2 {
		t.Fatalf("documentChanges: %+v, %v", got, err)
	}

	// A file operation is an error, not skipped.
	_, err = parseWorkspaceEdit([]byte(`{"documentChanges":[
		{"kind":"rename","oldUri":"file:///a.go","newUri":"file:///b.go"}]}`))
	if !errors.Is(err, ErrFileOperations) {
		t.Fatalf("file operation: got %v, want ErrFileOperations", err)
	}

	if _, err := parseWorkspaceEdit([]byte(`{`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestSplitRowsAndPosIn(t *testing.T) {
	rows := splitRows([]byte("aあ😀b\r\nsecond\n"))
	if len(rows) != 3 || string(rows[0]) != "aあ😀b" || string(rows[1]) != "second" || len(rows[2]) != 0 {
		t.Fatalf("rows = %q", rows)
	}
	if got := splitRows(nil); len(got) != 1 {
		t.Fatalf("empty content must give one empty row, got %d rows", len(got))
	}

	if got := posIn(rows, Position{0, 4}, PositionEncodingUTF16); got != (Pos{0, 8}) {
		t.Errorf("utf-16: %+v", got)
	}
	if got := posIn(rows, Position{1, 99}, PositionEncodingUTF16); got != (Pos{1, 6}) {
		t.Errorf("past the end of a line: %+v", got)
	}
	if got := posIn(rows, Position{9, 0}, PositionEncodingUTF16); got != (Pos{2, 0}) {
		t.Errorf("past the last line: %+v", got)
	}
}

func TestFinishRenameChecksVersions(t *testing.T) {
	buf := newFakeBuffer("/x/a.go", "abc")
	s := &session{buf: buf}
	m := &Manager{byURI: map[string]*session{"file:///x/a.go": s}}

	file := []wireFileEdit{{
		URI:   "file:///x/a.go",
		Edits: []wireTextEdit{{Range: &Range{Start: Position{0, 1}, End: Position{0, 2}}, NewText: "Z"}},
	}}

	// The buffer is at the recorded version: accepted, marked as open.
	we, err := m.finishRename(file, map[*session]uint64{s: 0}, PositionEncodingUTF8)
	if err != nil || len(we.Files) != 1 || !we.Files[0].Open || we.Files[0].Version != 0 ||
		we.Files[0].Edits[0].Start != (Pos{0, 1}) {
		t.Fatalf("current version: %+v, %v", we, err)
	}

	// The user typed meanwhile: the edits no longer fit.
	buf.insert(0, 0, "Q")
	if _, err := m.finishRename(file, map[*session]uint64{s: 0}, PositionEncodingUTF8); !errors.Is(err, ErrStale) {
		t.Fatalf("changed buffer: got %v, want ErrStale", err)
	}

	// A buffer that was not recorded at all is not trusted either.
	if _, err := m.finishRename(file, map[*session]uint64{}, PositionEncodingUTF8); !errors.Is(err, ErrStale) {
		t.Fatalf("unknown buffer: got %v, want ErrStale", err)
	}
}

func TestFinishRenameReadsClosedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.go")
	if err := os.WriteFile(path, []byte("one\nfoo bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{byURI: map[string]*session{}}

	we, err := m.finishRename([]wireFileEdit{{
		URI:   FileURI(path),
		Edits: []wireTextEdit{{Range: &Range{Start: Position{1, 4}, End: Position{1, 7}}, NewText: "x"}},
	}}, nil, PositionEncodingUTF16)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileEdit{{Path: path, Edits: []TextEdit{{Start: Pos{1, 4}, End: Pos{1, 7}, NewText: "x"}}}}
	if !reflect.DeepEqual(we.Files, want) {
		t.Fatalf("got %+v, want %+v", we.Files, want)
	}

	// A file that cannot be read is an error: no partial result.
	_, err = m.finishRename([]wireFileEdit{{URI: "file:///nonexistent/x.go"}}, nil, PositionEncodingUTF8)
	if err == nil {
		t.Fatal("an unreadable file must fail the whole rename")
	}
}

func TestManagerRename(t *testing.T) {
	h := newHarness(t)
	path := h.path("a.go")
	other := h.path("other.go")
	if err := os.WriteFile(other, []byte("package a\nfoo bar baz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := newFakeBuffer(path, "package a", "var x int")
	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	var (
		we   *WorkspaceEdit
		err  error
		done bool
	)
	h.m.Rename(buf, Pos{Row: 1, Col: 4}, "y", func(w *WorkspaceEdit, e error) {
		we, err, done = w, e, true
	})
	h.waitUntil("rename result", func() bool { return done })
	if err != nil {
		t.Fatal(err)
	}

	want := []FileEdit{
		{
			Path: path, Open: true, Version: 0,
			Edits: []TextEdit{{Start: Pos{1, 4}, End: Pos{1, 5}, NewText: "y"}},
		},
		{
			Path:  other,
			Edits: []TextEdit{{Start: Pos{1, 4}, End: Pos{1, 7}, NewText: "y"}},
		},
	}
	if !reflect.DeepEqual(we.Files, want) {
		t.Fatalf("got  %+v\nwant %+v", we.Files, want)
	}
}

func TestManagerRenameSendsOtherDocumentsEditsFirst(t *testing.T) {
	h := newHarness(t)
	a := newFakeBuffer(h.path("a.go"), "package a", "var x int")
	b := newFakeBuffer(h.path("b.go"), "package b")
	if err := os.WriteFile(h.path("other.go"), []byte("package a\nfoo bar baz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.m.Open(a, "go"); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Open(b, "go"); err != nil {
		t.Fatal(err)
	}

	// An edit in another document, then the rename, back to back: the server
	// must have the edit when it computes the rename (the fake server marks
	// the new name when it has seen it).
	b.insert(0, 0, "ZZ")

	var (
		we   *WorkspaceEdit
		err  error
		done bool
	)
	h.m.Rename(a, Pos{Row: 1, Col: 4}, "y", func(w *WorkspaceEdit, e error) {
		we, err, done = w, e, true
	})
	h.waitUntil("rename result", func() bool { return done })
	if err != nil {
		t.Fatal(err)
	}

	if got := we.Files[0].Edits[0].NewText; got != "y+ZZ" {
		t.Fatalf("new text = %q: the server computed the rename before it had the other document's edit", got)
	}
}

func TestManagerRenameOnUnopenedBuffer(t *testing.T) {
	h := newHarness(t)
	var err error
	h.m.Rename(newFakeBuffer(h.path("a.go"), "x"), Pos{}, "y", func(_ *WorkspaceEdit, e error) { err = e })
	if !errors.Is(err, ErrNotOpen) {
		t.Fatalf("got %v, want ErrNotOpen", err)
	}
}
