package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSymbolKindName(t *testing.T) {
	for kind, want := range map[int]string{12: "func", 23: "struct", 8: "field", 0: "", 99: ""} {
		if got := symbolKindName(kind); got != want {
			t.Errorf("symbolKindName(%d) = %q, want %q", kind, got, want)
		}
	}
}

func TestParseSymbolsTree(t *testing.T) {
	got, err := parseSymbols(json.RawMessage(`[
		{"name":"Outer","kind":23,
		 "range":{"start":{"line":0,"character":0},"end":{"line":9,"character":1}},
		 "selectionRange":{"start":{"line":0,"character":5},"end":{"line":0,"character":10}},
		 "children":[
		   {"name":"a","kind":8,"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":5}},
		    "selectionRange":{"start":{"line":1,"character":1},"end":{"line":1,"character":2}}},
		   {"name":"M","kind":6,"range":{"start":{"line":2,"character":0},"end":{"line":4,"character":1}},
		    "children":[{"name":"deep","kind":13,"range":{"start":{"line":3,"character":2},"end":{"line":3,"character":6}}}]}
		 ]},
		{"name":"Next","kind":12,"range":{"start":{"line":10,"character":0},"end":{"line":12,"character":1}}}
	]`))
	if err != nil {
		t.Fatal(err)
	}

	type brief struct {
		Name, Container string
		Depth           int
		Pos             Position
	}
	var gotBrief []brief
	for _, f := range got {
		gotBrief = append(gotBrief, brief{f.Name, f.Container, f.Depth, f.Pos})
	}
	want := []brief{
		{"Outer", "", 0, Position{0, 5}}, // the selection range: the name
		{"a", "Outer", 1, Position{1, 1}},
		{"M", "Outer", 1, Position{2, 0}}, // no selection range: the range
		{"deep", "M", 2, Position{3, 2}},
		{"Next", "", 0, Position{10, 0}},
	}
	if !reflect.DeepEqual(gotBrief, want) {
		t.Fatalf("got  %+v\nwant %+v", gotBrief, want)
	}
}

func TestParseSymbolsFlatAndEmpty(t *testing.T) {
	if got, err := parseSymbols(nil); err != nil || got != nil {
		t.Fatalf("nil: %v %v", got, err)
	}
	if got, err := parseSymbols(json.RawMessage(`null`)); err != nil || got != nil {
		t.Fatalf("null: %v %v", got, err)
	}

	got, err := parseSymbols(json.RawMessage(`[
		{"name":"Foo","kind":12,"containerName":"pkg",
		 "location":{"uri":"file:///a.go","range":{"start":{"line":3,"character":4},"end":{"line":3,"character":7}}}},
		{"name":"NoRange","kind":13,"location":{"uri":"file:///b.go"}}
	]`))
	if err != nil || len(got) != 2 {
		t.Fatalf("flat: %+v, %v", got, err)
	}
	if got[0].URI != "file:///a.go" || got[0].Pos != (Position{3, 4}) || got[0].Container != "pkg" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].URI != "file:///b.go" || got[1].Pos != (Position{}) {
		t.Errorf("a WorkspaceSymbol without a range = %+v", got[1])
	}

	if _, err := parseSymbols(json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestFormatResultChecksTheVersion(t *testing.T) {
	doc := NewDocument("u", "go", capsFor(PositionEncodingUTF8, SyncIncremental), 4, rows("abcd"))
	wire := []wireTextEdit{
		{Range: &Range{Start: Position{0, 1}, End: Position{0, 3}}, NewText: "X"},
		{NewText: "ignored: no range"},
	}

	res, err := formatResult(doc, 4, wire)
	if err != nil || res.Version != 4 || len(res.Edits) != 1 ||
		res.Edits[0] != (TextEdit{Start: Pos{0, 1}, End: Pos{0, 3}, NewText: "X"}) {
		t.Fatalf("current version: %+v, %v", res, err)
	}

	// The document moved on after the request was sent.
	if _, err := formatResult(doc, 3, wire); err == nil {
		t.Fatal("a result for an older version must be refused")
	}

	// No edits: already formatted.
	if res, err := formatResult(doc, 4, nil); err != nil || len(res.Edits) != 0 {
		t.Fatalf("no edits: %+v, %v", res, err)
	}
}

func TestManagerDocumentSymbols(t *testing.T) {
	h := newHarness(t)
	path := h.path("a.go")
	buf := newFakeBuffer(path, "type Outer struct {", "\tinner int", "}")
	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	var (
		got  []Symbol
		err  error
		done bool
	)
	h.m.DocumentSymbols(buf, func(s []Symbol, e error) { got, err, done = s, e, true })
	h.waitUntil("document symbols", func() bool { return done })

	want := []Symbol{
		{Name: "Outer", Kind: "struct", Depth: 0,
			Location: Location{Path: path, Start: Pos{0, 5}, Text: "type Outer struct {"}},
		{Name: "inner", Kind: "field", Container: "Outer", Depth: 1,
			Location: Location{Path: path, Start: Pos{1, 1}, Text: "inner int"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v, %v\nwant %+v", got, err, want)
	}
}

func TestManagerFormat(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "abcd efgh")
	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	var (
		res  FormatResult
		err  error
		done bool
	)
	h.m.Format(buf, FormatOptions{TabSize: 4, InsertSpaces: true}, func(r FormatResult, e error) {
		res, err, done = r, e, true
	})
	h.waitUntil("format result", func() bool { return done })

	want := FormatResult{Version: 0, Edits: []TextEdit{{Start: Pos{0, 0}, End: Pos{0, 4}, NewText: "FMT"}}}
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("got %+v, %v; want %+v", res, err, want)
	}
}

func TestManagerWorkspaceSymbols(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(other, []byte("package a\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fake server reads where the other file is from its environment.
	t.Setenv("FAKE_SYMBOL_OTHER_URI", FileURI(other))

	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "package a")
	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	var (
		got  []Symbol
		err  error
		done bool
	)
	h.m.WorkspaceSymbols(buf, "Foo", func(s []Symbol, e error) { got, err, done = s, e, true })
	h.waitUntil("workspace symbols", func() bool { return done })

	want := []Symbol{
		{Name: "Foo", Kind: "func", Container: "pkg",
			Location: Location{Path: other, Start: Pos{1, 2}, Text: "func Foo() {}"}},
		{Name: "NoRange", Kind: "var",
			Location: Location{Path: other, Start: Pos{0, 0}, Text: "package a"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v, %v\nwant %+v", got, err, want)
	}
}
