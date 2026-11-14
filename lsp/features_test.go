package lsp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHoverPlainText(t *testing.T) {
	for name, tc := range map[string]struct {
		contents string
		want     string
	}{
		"markup content": {
			`{"kind":"markdown","value":"` + "```go\\nfunc f()\\n```\\nDoc text" + `"}`,
			"func f()\nDoc text",
		},
		"plain string": {`"just text"`, "just text"},
		"marked string object": {
			`{"language":"go","value":"var x int"}`,
			"var x int",
		},
		"array": {
			`["first", {"language":"go","value":"second"}]`,
			"first\nsecond",
		},
		"empty": {`""`, ""},
		"absent": {``, ""},
	} {
		h := &hoverResponse{Contents: json.RawMessage(tc.contents)}
		if got := h.plainText(); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestParseLocations(t *testing.T) {
	r := func(l, c1, c2 uint32) Range {
		return Range{Start: Position{l, c1}, End: Position{l, c2}}
	}

	got, err := parseLocations(nil)
	if err != nil || got != nil {
		t.Fatalf("nil: %v, %v", got, err)
	}
	got, err = parseLocations(json.RawMessage(`null`))
	if err != nil || got != nil {
		t.Fatalf("null: %v, %v", got, err)
	}

	// A single Location.
	got, err = parseLocations(json.RawMessage(
		`{"uri":"file:///a.go","range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}}}`))
	if err != nil || len(got) != 1 || got[0].URI != "file:///a.go" || got[0].Range != r(1, 2, 5) {
		t.Fatalf("single: %+v, %v", got, err)
	}

	// An array of LocationLink: the selection range wins over the target range.
	got, err = parseLocations(json.RawMessage(`[{
		"targetUri":"file:///b.go",
		"targetRange":{"start":{"line":10,"character":0},"end":{"line":20,"character":1}},
		"targetSelectionRange":{"start":{"line":10,"character":5},"end":{"line":10,"character":8}}
	}]`))
	if err != nil || len(got) != 1 || got[0].URI != "file:///b.go" || got[0].Range != r(10, 5, 8) {
		t.Fatalf("link: %+v, %v", got, err)
	}

	if _, err := parseLocations(json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestFileColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.go")
	// Line 0 is "aあ😀b" (bytes: a, あ at 1..4, 😀 at 4..8, b at 8); CRLF endings.
	if err := os.WriteFile(path, []byte("aあ😀b\r\nsecond\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		pos  Position
		enc  string
		want Pos
	}{
		{Position{0, 4}, PositionEncodingUTF16, Pos{0, 8}},  // after 😀
		{Position{0, 3}, PositionEncodingUTF32, Pos{0, 8}},  // after 😀
		{Position{1, 3}, PositionEncodingUTF16, Pos{1, 3}},  // ASCII line
		{Position{1, 99}, PositionEncodingUTF16, Pos{1, 6}}, // past the end of the line; CR not counted
		{Position{9, 0}, PositionEncodingUTF16, Pos{2, 0}},  // past the last line
		{Position{0, 4}, PositionEncodingUTF8, Pos{0, 4}},   // utf-8: as is
	} {
		if got := fileColumn(path, tc.pos, tc.enc); got != tc.want {
			t.Errorf("fileColumn(%+v, %s) = %+v, want %+v", tc.pos, tc.enc, got, tc.want)
		}
	}

	// An unreadable file falls back to the offsets as they are.
	if got := fileColumn("/nonexistent/x.go", Position{3, 7}, PositionEncodingUTF16); got != (Pos{3, 7}) {
		t.Errorf("fallback = %+v", got)
	}
}

// waitUntil serves the UI queue until cond holds.
func (h *harness) waitUntil(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.pump()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; log:\n%s", what, h.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagerHoverAndDefinition(t *testing.T) {
	h := newHarness(t)
	path := h.path("a.go")
	buf := newFakeBuffer(path, "package a", "func x() {}")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	// Hover.
	var (
		hoverText string
		hoverErr  error
		hoverDone bool
	)
	h.m.Hover(buf, Pos{Row: 1, Col: 5}, func(text string, err error) {
		hoverText, hoverErr, hoverDone = text, err, true
	})
	h.waitUntil("hover result", func() bool { return hoverDone })
	if hoverErr != nil || hoverText != "func f()\nDoc text" {
		t.Fatalf("hover = %q, %v", hoverText, hoverErr)
	}

	// Definition: one location in the document, one in a file that cannot
	// be read (kept as it is).
	var (
		locs    []Location
		defErr  error
		defDone bool
	)
	h.m.Definition(buf, Pos{Row: 1, Col: 5}, func(l []Location, err error) {
		locs, defErr, defDone = l, err, true
	})
	h.waitUntil("definition result", func() bool { return defDone })

	want := []Location{
		{Path: path, Start: Pos{Row: 0, Col: 1}, Text: "package a"},
		{Path: "/nonexistent/other.go", Start: Pos{Row: 1, Col: 2}},
	}
	if defErr != nil || !reflect.DeepEqual(locs, want) {
		t.Fatalf("definition = %+v, %v; want %+v", locs, defErr, want)
	}
}

func TestManagerRequestSeesPendingEdits(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "abc")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}

	// An edit and a question, back to back, before the server may even be
	// up: the edit must reach the server first.
	buf.insert(0, 0, "ZZ")
	done := false
	h.m.Hover(buf, Pos{Row: 0, Col: 3}, func(string, error) { done = true })
	h.waitUntil("hover result", func() bool { return done })

	// The edit is sent before the request is made, so it shows up at the
	// server no later than the answer to the question about it.
	h.waitLog(`"text":"ZZ"`)
}

func TestManagerRequestOnUnopenedBuffer(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "x")

	var hoverErr, defErr error
	h.m.Hover(buf, Pos{}, func(_ string, err error) { hoverErr = err })
	h.m.Definition(buf, Pos{}, func(_ []Location, err error) { defErr = err })

	if !errors.Is(hoverErr, ErrNotOpen) || !errors.Is(defErr, ErrNotOpen) {
		t.Fatalf("got %v and %v, want ErrNotOpen", hoverErr, defErr)
	}
	if h.m.Opened(buf) {
		t.Fatal("Opened must be false")
	}

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	if !h.m.Opened(buf) {
		t.Fatal("Opened must be true after Open")
	}
}

// ---- completion ----

func TestWordStart(t *testing.T) {
	for _, tc := range []struct {
		line string
		col  int // -1: the end of the line
		want int
	}{
		{"fmt.Pri", -1, 4},
		{"fmt.", -1, 4}, // nothing typed after the dot
		{"x := foo_bar2", -1, 5}, // underscores and digits belong to the word
		{"変数名abc", -1, 0},       // so do non-ASCII letters
		{"fmt.Pri", 5, 4},        // the cursor in the middle of a word
		{"  ", -1, 2},
		{"", -1, 0},
	} {
		line := []byte(tc.line)
		col := tc.col
		if col < 0 {
			col = len(line)
		}
		if got := wordStart(line, col); got != tc.want {
			t.Errorf("wordStart(%q, %d) = %d, want %d", tc.line, col, got, tc.want)
		}
	}
}

func TestParseCompletionForms(t *testing.T) {
	if got, err := parseCompletion(nil); err != nil || got != nil {
		t.Fatalf("nil: %v %v", got, err)
	}
	if got, err := parseCompletion(json.RawMessage(`null`)); err != nil || got != nil {
		t.Fatalf("null: %v %v", got, err)
	}

	arr, err := parseCompletion(json.RawMessage(`[{"label":"a"},{"label":"b"}]`))
	if err != nil || len(arr) != 2 || arr[1].Label != "b" {
		t.Fatalf("array: %+v %v", arr, err)
	}

	list, err := parseCompletion(json.RawMessage(`{"isIncomplete":true,"items":[{"label":"x","kind":6}]}`))
	if err != nil || len(list) != 1 || list[0].Label != "x" || list[0].Kind != 6 {
		t.Fatalf("list: %+v %v", list, err)
	}

	if _, err := parseCompletion(json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestConvertCompletion(t *testing.T) {
	doc := NewDocument("u", "go", capsFor(PositionEncodingUTF8, SyncIncremental), 0, rows("fmt.Pri", "x"))
	cursor := Pos{Row: 0, Col: 7}

	rng := func(l, c1, c2 uint32) *Range {
		return &Range{Start: Position{l, c1}, End: Position{l, c2}}
	}
	got := convertCompletion(doc, cursor, []wireCompletionItem{
		{Label: "Println", Kind: 3, SortText: "b", TextEdit: &wireTextEdit{Range: rng(0, 4, 7), NewText: "Println"},
			Additional: []wireTextEdit{{Range: rng(1, 0, 0), NewText: "import x\n"}}},
		{Label: "Print", Kind: 3, SortText: "a", InsertText: "Print("},
		{Label: "Snip", Kind: 15, SortText: "c", InsertText: "Snip(${1:x})", InsertTextFormat: 2},
		{Label: "Both", SortText: "d", TextEdit: &wireTextEdit{Replace: rng(0, 4, 7), Insert: rng(0, 4, 5), NewText: "Both"}},
	})

	labels := []string{}
	for _, it := range got {
		labels = append(labels, it.Label)
	}
	if !reflect.DeepEqual(labels, []string{"Print", "Println", "Snip", "Both"}) {
		t.Fatalf("order = %v, want it to follow sortText", labels)
	}

	// No textEdit: the identifier prefix before the cursor is replaced.
	if e := got[0].Edit; e.Start != (Pos{0, 4}) || e.End != cursor || e.NewText != "Print(" {
		t.Errorf("Print edit = %+v", e)
	}
	// An explicit textEdit wins, additional edits are converted too.
	if e := got[1].Edit; e.Start != (Pos{0, 4}) || e.End != (Pos{0, 7}) || e.NewText != "Println" {
		t.Errorf("Println edit = %+v", e)
	}
	if a := got[1].Additional; len(a) != 1 || a[0].Start != (Pos{1, 0}) || a[0].NewText != "import x\n" {
		t.Errorf("Println additional = %+v", a)
	}
	// A snippet is not inserted as text: the label stands in.
	if e := got[2].Edit; e.NewText != "Snip" || got[2].Kind != "snippet" {
		t.Errorf("Snip = %+v kind %q", e, got[2].Kind)
	}
	// InsertReplaceEdit: the replace range is used.
	if e := got[3].Edit; e.Start != (Pos{0, 4}) || e.End != (Pos{0, 7}) {
		t.Errorf("Both edit = %+v", e)
	}
}

func TestManagerCompletion(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "package a", "fm")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	var (
		items []CompletionItem
		err   error
		done  bool
	)
	h.m.Completion(buf, Pos{Row: 1, Col: 2}, func(i []CompletionItem, e error) {
		items, err, done = i, e, true
	})
	h.waitUntil("completion result", func() bool { return done })
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 3 || items[0].Label != "Print" || items[1].Label != "Println" || items[2].Label != "Snip" {
		t.Fatalf("items = %+v", items)
	}

	// Print has no textEdit: the prefix "fm" (bytes 0..2 of line 1) is replaced.
	if e := items[0].Edit; e.Start != (Pos{1, 0}) || e.End != (Pos{1, 2}) || e.NewText != "Print" {
		t.Errorf("Print edit = %+v", e)
	}
	// Println comes with an import edit on line 0.
	if a := items[1].Additional; len(a) != 1 || a[0].Start != (Pos{0, 0}) || a[0].NewText != "import \"fmt\"\n" {
		t.Errorf("Println additional = %+v", a)
	}
	if items[1].Kind != "func" || items[1].Detail != "func(a ...any)" {
		t.Errorf("Println kind/detail = %q / %q", items[1].Kind, items[1].Detail)
	}
}

// ---- references and location text ----

func TestShortLine(t *testing.T) {
	if got := shortLine([]byte("  \tx := 1  \r")); got != "x := 1" {
		t.Errorf("trim: %q", got)
	}

	long := strings.Repeat("あ", 100) // 300 bytes
	got := shortLine([]byte(long))
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > maxLocationText+len("…") {
		t.Errorf("long line: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestLineAt(t *testing.T) {
	data := []byte("one\r\ntwo\nthree")
	for row, want := range map[int]string{0: "one", 1: "two", 2: "three"} {
		if got := lineAt(data, row); string(got) != want {
			t.Errorf("lineAt(%d) = %q, want %q", row, got, want)
		}
	}
	if got := lineAt(data, 3); got != nil {
		t.Errorf("past the last line: %q", got)
	}
}

func TestConvertLocationsReadsEachFileOnceAndKeepsText(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(other, []byte("first\n  second line  \nthird"), 0o644); err != nil {
		t.Fatal(err)
	}

	doc := NewDocument(FileURI(filepath.Join(dir, "a.go")), "go", capsFor(PositionEncodingUTF8, SyncIncremental), 0,
		rows("package a", "var x int"))

	at := func(uri string, l, c uint32) wireLocation {
		return wireLocation{URI: uri, Range: Range{Start: Position{l, c}, End: Position{l, c + 1}}}
	}
	got := convertLocations(doc, []wireLocation{
		at(doc.uri, 1, 4),
		at(FileURI(other), 1, 2),
		at(FileURI(other), 2, 0),
		at("file:///nonexistent/x.go", 3, 4),
		at("https://example.com/x", 0, 0), // not a file: skipped
	})

	want := []Location{
		{Path: filepath.Join(dir, "a.go"), Start: Pos{1, 4}, Text: "var x int"},
		{Path: other, Start: Pos{1, 2}, Text: "second line"},
		{Path: other, Start: Pos{2, 0}, Text: "third"},
		{Path: "/nonexistent/x.go", Start: Pos{3, 4}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestManagerReferences(t *testing.T) {
	h := newHarness(t)
	path := h.path("a.go")
	buf := newFakeBuffer(path, "package a", "var x int")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	var (
		locs []Location
		err  error
		done bool
	)
	h.m.References(buf, Pos{Row: 1, Col: 4}, func(l []Location, e error) {
		locs, err, done = l, e, true
	})
	h.waitUntil("references result", func() bool { return done })

	want := []Location{
		{Path: path, Start: Pos{1, 5}, Text: "var x int"},
		{Path: path, Start: Pos{0, 0}, Text: "package a"},
	}
	if err != nil || !reflect.DeepEqual(locs, want) {
		t.Fatalf("references = %+v, %v; want %+v", locs, err, want)
	}

	// Not open: an error, not a hang.
	var notOpenErr error
	h.m.References(newFakeBuffer(h.path("b.go"), "x"), Pos{}, func(_ []Location, e error) { notOpenErr = e })
	if !errors.Is(notOpenErr, ErrNotOpen) {
		t.Fatalf("got %v, want ErrNotOpen", notOpenErr)
	}
}
