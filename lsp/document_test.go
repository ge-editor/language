package lsp

import (
	"encoding/json"
	"errors"
	"testing"
)

func rows(lines ...string) [][]byte {
	out := make([][]byte, len(lines))
	for i, l := range lines {
		out[i] = []byte(l)
	}
	return out
}

func capsFor(enc string, change SyncKind) ServerCapabilities {
	sync, _ := json.Marshal(map[string]any{
		"openClose": true,
		"change":    int(change),
		"save":      map[string]any{"includeText": true},
	})
	return ServerCapabilities{PositionEncoding: enc, TextDocumentSync: sync}
}

// ---- position conversion ----

func TestEncodeDecodeColumn(t *testing.T) {
	// bytes: a(0) あ(1..4) 😀(4..8) b(8), 9 bytes in total.
	line := []byte("aあ😀b")

	enc := []struct {
		enc  string
		byte []int // byte columns at rune boundaries
		char []uint32
	}{
		{PositionEncodingUTF8, []int{0, 1, 4, 8, 9}, []uint32{0, 1, 4, 8, 9}},
		{PositionEncodingUTF16, []int{0, 1, 4, 8, 9}, []uint32{0, 1, 2, 4, 5}},
		{PositionEncodingUTF32, []int{0, 1, 4, 8, 9}, []uint32{0, 1, 2, 3, 4}},
	}
	for _, e := range enc {
		for i := range e.byte {
			if got := encodeColumn(line, e.byte[i], e.enc); got != e.char[i] {
				t.Errorf("%s: encode(%d) = %d, want %d", e.enc, e.byte[i], got, e.char[i])
			}
			if got := decodeColumn(line, e.char[i], e.enc); got != e.byte[i] {
				t.Errorf("%s: decode(%d) = %d, want %d", e.enc, e.char[i], got, e.byte[i])
			}
		}
	}
}

func TestColumnClampingAndSurrogates(t *testing.T) {
	line := []byte("aあ😀b")

	// Past the end: the end of the line.
	if got := decodeColumn(line, 99, PositionEncodingUTF16); got != 9 {
		t.Errorf("decode past end = %d, want 9", got)
	}
	if got := decodeColumn(line, 99, PositionEncodingUTF8); got != 9 {
		t.Errorf("utf-8 decode past end = %d, want 9", got)
	}
	if got := encodeColumn(line, 99, PositionEncodingUTF16); got != 5 {
		t.Errorf("encode past end = %d, want 5", got)
	}

	// utf-16 character 3 is between the two halves of 😀: round down.
	if got := decodeColumn(line, 3, PositionEncodingUTF16); got != 4 {
		t.Errorf("decode mid-surrogate = %d, want 4", got)
	}

	// Invalid UTF-8 counts one unit per byte and stays consistent.
	bad := []byte{'a', 0xff, 'b'}
	if got := encodeColumn(bad, 2, PositionEncodingUTF16); got != 2 {
		t.Errorf("encode invalid = %d, want 2", got)
	}
	if got := decodeColumn(bad, 2, PositionEncodingUTF16); got != 2 {
		t.Errorf("decode invalid = %d, want 2", got)
	}
}

// ---- Document ----

func TestOpenParams(t *testing.T) {
	d := NewDocument("file:///a.go", "go", capsFor(PositionEncodingUTF8, SyncIncremental), 7, rows("package a", ""))
	p := d.OpenParams().TextDocument
	if p.URI != "file:///a.go" || p.LanguageID != "go" || p.Version != 7 || p.Text != "package a\n" {
		t.Fatalf("OpenParams = %+v", p)
	}
}

func TestIncrementalEdits(t *testing.T) {
	d := NewDocument("file:///a", "go", capsFor(PositionEncodingUTF8, SyncIncremental), 0, rows("hello", "world"))

	check := func(name string, c Change, wantRange Range, wantText, wantDoc string) {
		t.Helper()
		p, err := d.Apply(c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p == nil || len(p.ContentChanges) != 1 {
			t.Fatalf("%s: params = %+v", name, p)
		}
		cc := p.ContentChanges[0]
		if cc.Range == nil || *cc.Range != wantRange {
			t.Errorf("%s: range = %+v, want %+v", name, cc.Range, wantRange)
		}
		if cc.Text != wantText {
			t.Errorf("%s: text = %q, want %q", name, cc.Text, wantText)
		}
		if p.TextDocument.Version != int32(c.Version) {
			t.Errorf("%s: version = %d, want %d", name, p.TextDocument.Version, c.Version)
		}
		if got := string(d.Text()); got != wantDoc {
			t.Errorf("%s: mirror = %q, want %q", name, got, wantDoc)
		}
	}

	pos := func(l, c uint32) Position { return Position{Line: l, Character: c} }

	check("insert", Change{Version: 1, Kind: ChangeInsert, Start: Pos{0, 5}, Text: rows(" there")},
		Range{pos(0, 5), pos(0, 5)}, " there", "hello there\nworld")

	check("delete", Change{Version: 2, Kind: ChangeDelete, Start: Pos{0, 5}, OldEnd: Pos{0, 11}},
		Range{pos(0, 5), pos(0, 11)}, "", "hello\nworld")

	check("insert lines", Change{Version: 3, Kind: ChangeInsert, Start: Pos{0, 5}, Text: rows("", "foo", "bar")},
		Range{pos(0, 5), pos(0, 5)}, "\nfoo\nbar", "hello\nfoo\nbar\nworld")

	check("delete across lines", Change{Version: 4, Kind: ChangeDelete, Start: Pos{0, 3}, OldEnd: Pos{2, 1}},
		Range{pos(0, 3), pos(2, 1)}, "", "helar\nworld")

	if d.Version() != 4 || d.NumLines() != 2 {
		t.Fatalf("Version = %d, NumLines = %d", d.Version(), d.NumLines())
	}
}

func TestInsertInMiddleOfLine(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncIncremental), 0, rows("abcd"))

	// "X\nY" in the middle: "ab" + "X" / "Y" + "cd".
	if _, err := d.Apply(Change{Version: 1, Kind: ChangeInsert, Start: Pos{0, 2}, Text: rows("X", "Y")}); err != nil {
		t.Fatal(err)
	}
	if got := string(d.Text()); got != "abX\nYcd" {
		t.Fatalf("mirror = %q", got)
	}
}

func TestUTF16Ranges(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF16, SyncIncremental), 0, rows("あ😀x"))

	// Insert before x (byte 7): utf-16 character 3.
	p, err := d.Apply(Change{Version: 1, Kind: ChangeInsert, Start: Pos{0, 7}, Text: rows("y")})
	if err != nil {
		t.Fatal(err)
	}
	if r := p.ContentChanges[0].Range; r.Start != (Position{0, 3}) || r.End != (Position{0, 3}) {
		t.Fatalf("insert range = %+v", r)
	}

	// Delete "あ😀" (bytes 0..7): utf-16 range 0..3.
	p, err = d.Apply(Change{Version: 2, Kind: ChangeDelete, Start: Pos{0, 0}, OldEnd: Pos{0, 7}})
	if err != nil {
		t.Fatal(err)
	}
	if r := p.ContentChanges[0].Range; r.Start != (Position{0, 0}) || r.End != (Position{0, 3}) {
		t.Fatalf("delete range = %+v", r)
	}
	if got := string(d.Text()); got != "yx" {
		t.Fatalf("mirror = %q", got)
	}
}

func TestResetSendsFullText(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncIncremental), 0, rows("old"))

	p, err := d.Apply(Change{Version: 1, Kind: ChangeReset, Text: rows("new", "text")})
	if err != nil {
		t.Fatal(err)
	}
	cc := p.ContentChanges[0]
	if cc.Range != nil || cc.Text != "new\ntext" {
		t.Fatalf("reset change = %+v", cc)
	}
}

func TestFullSyncKind(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncFull), 0, rows("ab"))

	p, err := d.Apply(Change{Version: 1, Kind: ChangeInsert, Start: Pos{0, 1}, Text: rows("X")})
	if err != nil {
		t.Fatal(err)
	}
	cc := p.ContentChanges[0]
	if cc.Range != nil || cc.Text != "aXb" {
		t.Fatalf("full sync change = %+v", cc)
	}
}

func TestNoneSyncKindSendsNothing(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncNone), 0, rows("ab"))

	p, err := d.Apply(Change{Version: 1, Kind: ChangeInsert, Start: Pos{0, 1}, Text: rows("X")})
	if err != nil || p != nil {
		t.Fatalf("got %+v, %v; want nil, nil", p, err)
	}
	if d.Version() != 1 || string(d.Text()) != "aXb" {
		t.Fatalf("mirror not kept in step: v%d %q", d.Version(), d.Text())
	}
}

func TestStaleChangeIsIgnored(t *testing.T) {
	// Opened at version 5: changes up to 5 are already in the open text.
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncIncremental), 5, rows("ab"))

	p, err := d.Apply(Change{Version: 5, Kind: ChangeInsert, Start: Pos{0, 0}, Text: rows("Z")})
	if err != nil || p != nil {
		t.Fatalf("got %+v, %v; want nil, nil", p, err)
	}
	if string(d.Text()) != "ab" {
		t.Fatalf("stale change modified the mirror: %q", d.Text())
	}
}

func TestGapDesyncsUntilResync(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncIncremental), 0, rows("ab"))

	_, err := d.Apply(Change{Version: 2, Kind: ChangeInsert, Start: Pos{0, 0}, Text: rows("Z")})
	if !errors.Is(err, ErrDesynced) {
		t.Fatalf("gap: got %v, want ErrDesynced", err)
	}
	// Even a change that would now fit is refused.
	if _, err := d.Apply(Change{Version: 1, Kind: ChangeInsert, Start: Pos{0, 0}, Text: rows("Z")}); !errors.Is(err, ErrDesynced) {
		t.Fatalf("after desync: got %v, want ErrDesynced", err)
	}

	p := d.Resync(9, rows("fresh"))
	if p == nil || p.ContentChanges[0].Text != "fresh" || p.TextDocument.Version != 9 {
		t.Fatalf("Resync params = %+v", p)
	}

	p, err = d.Apply(Change{Version: 10, Kind: ChangeInsert, Start: Pos{0, 5}, Text: rows("!")})
	if err != nil || p == nil {
		t.Fatalf("after Resync: %+v, %v", p, err)
	}
	if string(d.Text()) != "fresh!" {
		t.Fatalf("mirror = %q", d.Text())
	}
}

func TestInvalidPositionsDesync(t *testing.T) {
	bad := []Change{
		{Version: 1, Kind: ChangeInsert, Start: Pos{5, 0}, Text: rows("x")},
		{Version: 1, Kind: ChangeInsert, Start: Pos{0, 99}, Text: rows("x")},
		{Version: 1, Kind: ChangeDelete, Start: Pos{0, 1}, OldEnd: Pos{0, 99}},
		{Version: 1, Kind: ChangeDelete, Start: Pos{0, 2}, OldEnd: Pos{0, 1}}, // reversed
	}
	for i, c := range bad {
		d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncIncremental), 0, rows("ab"))
		if _, err := d.Apply(c); !errors.Is(err, ErrDesynced) {
			t.Errorf("case %d: got %v, want ErrDesynced", i, err)
		}
	}
}

func TestMirrorDoesNotAliasInput(t *testing.T) {
	src := rows("abc")
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncIncremental), 0, src)
	src[0][0] = 'Z'
	if string(d.Text()) != "abc" {
		t.Fatalf("mirror aliases the caller's rows: %q", d.Text())
	}

	ins := rows("X")
	if _, err := d.Apply(Change{Version: 1, Kind: ChangeInsert, Start: Pos{0, 1}, Text: ins}); err != nil {
		t.Fatal(err)
	}
	ins[0][0] = 'Q'
	if string(d.Text()) != "aXbc" {
		t.Fatalf("mirror aliases the change text: %q", d.Text())
	}
}

func TestSaveAndCloseParams(t *testing.T) {
	d := NewDocument("file:///a", "go", capsFor(PositionEncodingUTF8, SyncIncremental), 0, rows("x"))

	p, ok := d.SaveParams()
	if !ok || p.Text == nil || *p.Text != "x" {
		t.Fatalf("SaveParams = %+v, %v", p, ok)
	}
	if got := d.CloseParams().TextDocument.URI; got != "file:///a" {
		t.Fatalf("CloseParams uri = %q", got)
	}

	// A server that did not ask for didSave.
	noSave := ServerCapabilities{TextDocumentSync: json.RawMessage(`{"openClose":true,"change":2}`)}
	d2 := NewDocument("u", "x", noSave, 0, rows("x"))
	if _, ok := d2.SaveParams(); ok {
		t.Fatal("SaveParams must report false when the server wants no didSave")
	}
}

func TestToFromLSP(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF16, SyncIncremental), 0, rows("aあ😀b", "xyz"))

	if got := d.ToLSP(Pos{0, 8}); got != (Position{0, 4}) {
		t.Errorf("ToLSP = %+v", got)
	}
	if got := d.FromLSP(Position{0, 4}); got != (Pos{0, 8}) {
		t.Errorf("FromLSP = %+v", got)
	}
	// Past the last line: the end of the text.
	if got := d.FromLSP(Position{9, 0}); got != (Pos{1, 3}) {
		t.Errorf("FromLSP past end = %+v", got)
	}
}

func TestConvertDiagnostics(t *testing.T) {
	d := NewDocument("u", "x", capsFor(PositionEncodingUTF16, SyncIncremental), 0, rows("aあ😀b"))

	pos := func(l, c uint32) Position { return Position{Line: l, Character: c} }
	list := []wireDiagnostic{
		{Range: Range{pos(0, 1), pos(0, 4)}, Severity: 2, Code: json.RawMessage(`7`), Source: "s", Message: "m1"},
		{Range: Range{pos(0, 0), pos(0, 0)}, Severity: 0, Code: json.RawMessage(`"E1"`), Message: "m2"},
		{Range: Range{pos(0, 0), pos(0, 1)}, Severity: 9, Message: "m3"},
	}
	got := d.convertDiagnostics(list)
	if len(got) != 3 {
		t.Fatalf("got %d diagnostics", len(got))
	}

	// utf-16 characters 1..4 are bytes 1..8 (あ and 😀).
	if got[0].Start != (Pos{0, 1}) || got[0].End != (Pos{0, 8}) ||
		got[0].Severity != SeverityWarning || got[0].Code != "7" || got[0].Source != "s" || got[0].Message != "m1" {
		t.Errorf("first = %+v", got[0])
	}
	// Severity outside 1..4 reads as an error; a string code is unquoted.
	if got[1].Severity != SeverityError || got[1].Code != "E1" || got[1].Start != got[1].End {
		t.Errorf("second = %+v", got[1])
	}
	if got[2].Severity != SeverityError || got[2].Code != "" {
		t.Errorf("third = %+v", got[2])
	}
}

func TestPublishDiagnosticsDropsStaleVersions(t *testing.T) {
	var got []DiagnosticsUpdate
	m := &Manager{opts: ManagerOptions{
		Dispatch:      func(f func()) { f() },
		OnDiagnostics: func(_ Buffer, u DiagnosticsUpdate) { got = append(got, u) },
	}}
	s := &session{m: m}
	doc := NewDocument("u", "x", capsFor(PositionEncodingUTF8, SyncIncremental), 3, rows("abc"))

	one := []wireDiagnostic{{Range: Range{Position{0, 0}, Position{0, 2}}, Severity: 1, Message: "m"}}
	v := func(n int32) *int32 { return &n }

	s.publishDiagnostics(doc, &publishDiagnosticsParams{Version: v(2), Diagnostics: one}, false)
	if len(got) != 0 {
		t.Fatal("a result for older text must be dropped")
	}

	s.publishDiagnostics(doc, &publishDiagnosticsParams{Version: v(3), Diagnostics: one}, true)
	if len(got) != 0 {
		t.Fatal("nothing may be published while resyncing")
	}

	s.publishDiagnostics(doc, &publishDiagnosticsParams{Version: v(3), Diagnostics: one}, false)
	if len(got) != 1 || got[0].Version != 3 || len(got[0].Items) != 1 || got[0].Items[0].End != (Pos{0, 2}) {
		t.Fatalf("current-version result: %+v", got)
	}

	// A server without version support: taken as current.
	s.publishDiagnostics(doc, &publishDiagnosticsParams{Diagnostics: nil}, false)
	if len(got) != 2 || len(got[1].Items) != 0 {
		t.Fatalf("unversioned empty result: %+v", got)
	}
}

