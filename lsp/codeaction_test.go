package lsp

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRangesTouch(t *testing.T) {
	r := func(l1, c1, l2, c2 uint32) Range {
		return Range{Start: Position{l1, c1}, End: Position{l2, c2}}
	}
	for name, tc := range map[string]struct {
		a, b Range
		want bool
	}{
		"cursor inside":        {r(1, 2, 1, 8), r(1, 4, 1, 4), true},
		"cursor at the end":    {r(1, 2, 1, 8), r(1, 8, 1, 8), true}, // right behind the identifier
		"cursor at the start":  {r(1, 2, 1, 8), r(1, 2, 1, 2), true},
		"cursor before":        {r(1, 2, 1, 8), r(1, 1, 1, 1), false},
		"cursor after":         {r(1, 2, 1, 8), r(1, 9, 1, 9), false},
		"other line":           {r(1, 2, 1, 8), r(2, 4, 2, 4), false},
		"selection overlaps":   {r(1, 2, 1, 8), r(0, 0, 1, 3), true},
		"multi-line contains":  {r(1, 0, 4, 0), r(2, 5, 3, 1), true},
		"selection before":     {r(5, 0, 5, 4), r(0, 0, 4, 99), false},
	} {
		if got := rangesTouch(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestParseCodeActions(t *testing.T) {
	if got, n, err := parseCodeActions(nil); err != nil || got != nil || n != 0 {
		t.Fatalf("nil: %v %d %v", got, n, err)
	}
	if got, n, err := parseCodeActions(json.RawMessage(`null`)); err != nil || got != nil || n != 0 {
		t.Fatalf("null: %v %d %v", got, n, err)
	}

	edit := `{"changes":{"file:///a.go":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"newText":"x"}]}}`
	got, skipped, err := parseCodeActions(json.RawMessage(`[
		{"title":"Fix","kind":"quickfix","isPreferred":true,"edit":` + edit + `},
		{"title":"Command only","command":"do.it","arguments":[1]},
		{"title":"Command in action","kind":"refactor","command":{"title":"c","command":"do.it"}},
		{"title":"Disabled","disabled":{"reason":"r"},"edit":` + edit + `},
		{"title":"Lazy","kind":"refactor"},
		{"title":"Files","edit":{"documentChanges":[{"kind":"delete","uri":"file:///a.go"}]}},
		{"title":"Empty","edit":{"changes":{}}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].title != "Fix" || got[0].kind != "quickfix" || !got[0].preferred ||
		len(got[0].files) != 1 || got[0].files[0].URI != "file:///a.go" {
		t.Fatalf("kept = %+v", got)
	}
	if skipped != 6 {
		t.Fatalf("skipped = %d, want 6 (command, command in action, disabled, lazy, file operation, empty)", skipped)
	}

	if _, _, err := parseCodeActions(json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestFinishCodeActions(t *testing.T) {
	buf := newFakeBuffer("/x/a.go", "abc")
	s := &session{buf: buf}
	m := &Manager{byURI: map[string]*session{"file:///x/a.go": s}}
	versions := map[*session]uint64{s: 0}

	file := func(uri string) []wireFileEdit {
		return []wireFileEdit{{URI: uri, Edits: []wireTextEdit{
			{Range: &Range{Start: Position{0, 0}, End: Position{0, 1}}, NewText: "Z"},
		}}}
	}

	wire := []codeActionWire{
		{title: "other", files: file("file:///x/a.go")},
		{title: "preferred", preferred: true, files: file("file:///x/a.go")},
		{title: "unreadable", files: file("file:///nonexistent/b.go")},
	}
	res, err := m.finishCodeActions(wire, 2, versions, PositionEncodingUTF8)
	if err != nil {
		t.Fatal(err)
	}

	var titles []string
	for _, a := range res.Actions {
		titles = append(titles, a.Title)
	}
	if !reflect.DeepEqual(titles, []string{"preferred", "other"}) {
		t.Errorf("titles = %v, want the preferred first", titles)
	}
	if res.Skipped != 3 { // 2 given, plus the one whose file could not be read
		t.Errorf("Skipped = %d, want 3", res.Skipped)
	}
	if e := res.Actions[0].Edit; e == nil || len(e.Files) != 1 || !e.Files[0].Open || e.Files[0].Edits[0].End != (Pos{0, 1}) {
		t.Errorf("edit = %+v", e)
	}

	// The buffer changed while the request ran: nothing is offered.
	buf.insert(0, 0, "Q")
	if _, err := m.finishCodeActions(wire[:1], 0, versions, PositionEncodingUTF8); !errors.Is(err, ErrStale) {
		t.Fatalf("got %v, want ErrStale", err)
	}
}

func TestManagerCodeActions(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "package a", "var x int")
	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitDiag(0) // the fake server's diagnostic at 0:0-0:3 has arrived

	var (
		res  CodeActionsResult
		err  error
		done bool
	)
	h.m.CodeActions(buf, Pos{Row: 0, Col: 1}, Pos{Row: 0, Col: 1}, func(r CodeActionsResult, e error) {
		res, err, done = r, e, true
	})
	h.waitUntil("code actions", func() bool { return done })
	if err != nil {
		t.Fatal(err)
	}

	// Two carry an edit; command-only, disabled, lazy and file operations are
	// skipped. The preferred one comes first, and the quick fix was asked for
	// with the diagnostic that is at the cursor.
	var titles []string
	for _, a := range res.Actions {
		titles = append(titles, a.Title)
	}
	if !reflect.DeepEqual(titles, []string{"Preferred", "Fix A (1 diagnostics)"}) {
		t.Fatalf("titles = %v", titles)
	}
	if res.Skipped != 4 {
		t.Fatalf("Skipped = %d, want 4", res.Skipped)
	}

	edit := res.Actions[0].Edit
	if len(edit.Files) != 1 || !edit.Files[0].Open || edit.Files[0].Version != 0 ||
		!reflect.DeepEqual(edit.Files[0].Edits, []TextEdit{{Start: Pos{0, 0}, End: Pos{0, 3}, NewText: "abc"}}) {
		t.Fatalf("edit = %+v", edit)
	}

	// Away from the diagnostic the quick fix is asked for without it.
	done = false
	h.m.CodeActions(buf, Pos{Row: 1, Col: 2}, Pos{Row: 1, Col: 2}, func(r CodeActionsResult, e error) {
		res, err, done = r, e, true
	})
	h.waitUntil("code actions, elsewhere", func() bool { return done })
	if err != nil || res.Actions[1].Title != "Fix A (0 diagnostics)" {
		t.Fatalf("elsewhere: %+v, %v", res.Actions, err)
	}
}
