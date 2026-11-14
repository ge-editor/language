package lsp

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
)

// Document synchronization (textDocument/didOpen, didChange, didSave,
// didClose).
//
// This file is independent of editorleaf: it takes edits in its own Change
// type. The code that owns the EditBuffer (the workspace side) converts
// editbuffer.Change into lsp.Change, which is a field-for-field copy.
//
// Document keeps a private mirror of the buffer text. It is needed because
// a position in the old text must be converted to the server's position
// encoding using the old line content, and a Delete change no longer
// carries that text. The mirror is updated by exactly the changes that
// are sent, so the server's view and the mirror cannot drift apart.
//
// A Document is NOT safe for concurrent use. The intended owner is one
// goroutine per document, fed through a channel by the EditBuffer change
// listener (which runs on the UI goroutine and must not block), the same
// shape as syntax.Worker.

// ErrDesynced means a Change did not fit the mirror: a version was
// skipped or a position was out of range. The Document refuses further
// changes until Resync gives it the whole buffer again.
var ErrDesynced = errors.New("lsp: document out of sync with buffer")

// Pos is a position in ge's coordinates: row and byte column.
type Pos struct {
	Row, Col int
}

// ChangeKind mirrors editbuffer.ChangeKind.
type ChangeKind int

const (
	// ChangeInsert: Text was inserted at Start.
	ChangeInsert ChangeKind = iota
	// ChangeDelete: the region [Start, OldEnd) was removed.
	ChangeDelete
	// ChangeReset: the whole content was replaced by Text.
	ChangeReset
)

// Change mirrors editbuffer.Change (NewEnd is not needed: it follows from
// Start and Text).
type Change struct {
	Version uint64
	Kind    ChangeKind
	Start   Pos
	OldEnd  Pos
	Text    [][]byte // inserted rows, or the replacement rows for ChangeReset
}

// Document is the client's view of one open text document.
type Document struct {
	uri        string
	languageID string
	enc        string
	sync       SyncOptions

	version  uint64 // version of the buffer state the mirror describes
	lines    [][]byte
	desynced bool
}

// NewDocument creates the mirror for a buffer whose current content is rows
// at the given buffer version. rows are copied.
//
// The text and the version must be read at the same moment on the UI
// goroutine (rows := buffer rows, version := eb.Version()), and the change
// listener must be registered in that same moment. The first Change then
// has version+1; Apply treats anything else as a gap.
func NewDocument(uri, languageID string, caps ServerCapabilities, version uint64, rows [][]byte) *Document {
	return &Document{
		uri:        uri,
		languageID: languageID,
		enc:        caps.Encoding(),
		sync:       caps.Sync(),
		version:    version,
		lines:      cloneLines(rows),
	}
}

// URI returns the document's file:// URI.
func (d *Document) URI() string { return d.uri }

// Version returns the buffer version the mirror currently describes.
func (d *Document) Version() uint64 { return d.version }

// Encoding returns the position encoding in use.
func (d *Document) Encoding() string { return d.enc }

// NumLines returns the number of lines in the mirror.
func (d *Document) NumLines() int { return len(d.lines) }

// Text returns the whole mirrored text, lines joined with '\n'.
func (d *Document) Text() []byte { return bytes.Join(d.lines, []byte{'\n'}) }

// ToLSP converts a ge position to an LSP position using the current text.
// A position outside the text is clamped to it.
func (d *Document) ToLSP(p Pos) Position {
	if len(d.lines) == 0 {
		return Position{}
	}
	row := min(max(p.Row, 0), len(d.lines)-1)
	return Position{
		Line:      uint32(row),
		Character: encodeColumn(d.lines[row], p.Col, d.enc),
	}
}

// FromLSP converts an LSP position (a diagnostic range, a definition
// target, ...) to a ge position using the current text. Positions past the
// end of a line or of the text are clamped, as the protocol specifies.
//
// The server's answer refers to the text version it had seen; if the user
// typed since, the caller must account for that (see the version fields in
// publishDiagnostics).
func (d *Document) FromLSP(p Position) Pos {
	if len(d.lines) == 0 {
		return Pos{}
	}
	if int64(p.Line) >= int64(len(d.lines)) {
		last := len(d.lines) - 1
		return Pos{Row: last, Col: len(d.lines[last])}
	}
	row := int(p.Line)
	return Pos{Row: row, Col: decodeColumn(d.lines[row], p.Character, d.enc)}
}

// ---- protocol parameter types ----

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier carries the version after the change. The
// protocol types it as a 32 bit integer; the buffer version is truncated
// to it, which takes 2^31 edits to notice.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int32  `json:"version"`
}

type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int32  `json:"version"`
	Text       string `json:"text"`
}

type DidOpenParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// ContentChange is one element of didChange.contentChanges. With Range set
// it replaces that range; without, it replaces the whole document.
type ContentChange struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

type DidChangeParams struct {
	TextDocument   VersionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []ContentChange                 `json:"contentChanges"`
}

type DidSaveParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

type DidCloseParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// ---- building notifications ----

// OpenParams returns the didOpen parameters for the current mirror.
func (d *Document) OpenParams() DidOpenParams {
	return DidOpenParams{TextDocument: TextDocumentItem{
		URI:        d.uri,
		LanguageID: d.languageID,
		Version:    int32(d.version),
		Text:       string(d.Text()),
	}}
}

// SaveParams returns the didSave parameters, or false when the server did
// not ask for didSave notifications.
func (d *Document) SaveParams() (DidSaveParams, bool) {
	if !d.sync.Save {
		return DidSaveParams{}, false
	}
	p := DidSaveParams{TextDocument: TextDocumentIdentifier{URI: d.uri}}
	if d.sync.SaveIncludeText {
		s := string(d.Text())
		p.Text = &s
	}
	return p, true
}

// CloseParams returns the didClose parameters.
func (d *Document) CloseParams() DidCloseParams {
	return DidCloseParams{TextDocument: TextDocumentIdentifier{URI: d.uri}}
}

// Apply updates the mirror with c and returns the didChange parameters to
// send, or nil when nothing needs to be sent:
//
//   - c.Version is not newer than the mirror (the change was already part
//     of the text sent in didOpen / Resync), or
//   - the server does not want changes (sync kind None).
//
// Changes must arrive in order and without gaps. A gap, or a position that
// does not fit the mirror, returns ErrDesynced and leaves the Document
// refusing everything until Resync; this is deliberate: a guess would let
// the server's text diverge silently, which breaks every later position.
func (d *Document) Apply(c Change) (*DidChangeParams, error) {
	if d.desynced {
		return nil, ErrDesynced
	}
	if c.Version <= d.version {
		return nil, nil
	}
	if c.Version != d.version+1 {
		d.desynced = true
		return nil, fmt.Errorf("%w: expected version %d, got %d", ErrDesynced, d.version+1, c.Version)
	}

	var change ContentChange
	switch c.Kind {
	case ChangeReset:
		d.lines = cloneLines(c.Text)
		change = ContentChange{Text: string(d.Text())}

	case ChangeInsert:
		if len(c.Text) == 0 {
			d.version = c.Version
			return nil, nil
		}
		if !d.valid(c.Start) {
			return nil, d.desync("insert at invalid position")
		}
		at := d.ToLSP(c.Start) // old text, before the mirror changes
		d.insert(c.Start, c.Text)
		change = ContentChange{
			Range: &Range{Start: at, End: at},
			Text:  string(bytes.Join(c.Text, []byte{'\n'})),
		}

	case ChangeDelete:
		if !d.valid(c.Start) || !d.valid(c.OldEnd) || posLess(c.OldEnd, c.Start) {
			return nil, d.desync("delete with invalid range")
		}
		r := Range{Start: d.ToLSP(c.Start), End: d.ToLSP(c.OldEnd)}
		d.remove(c.Start, c.OldEnd)
		change = ContentChange{Range: &r}

	default:
		return nil, d.desync("unknown change kind")
	}

	d.version = c.Version

	switch d.sync.Change {
	case SyncNone:
		return nil, nil
	case SyncFull:
		change = ContentChange{Text: string(d.Text())}
	}
	return d.changeParams([]ContentChange{change}), nil
}

// Resync replaces the mirror with rows at version and returns a full-text
// didChange. Use it after ErrDesynced, with a fresh snapshot of the buffer
// taken on the UI goroutine together with its Version. It works for every
// sync kind except None, where it returns nil.
func (d *Document) Resync(version uint64, rows [][]byte) *DidChangeParams {
	d.lines = cloneLines(rows)
	d.version = version
	d.desynced = false

	if d.sync.Change == SyncNone {
		return nil
	}
	return d.changeParams([]ContentChange{{Text: string(d.Text())}})
}

func (d *Document) changeParams(cc []ContentChange) *DidChangeParams {
	return &DidChangeParams{
		TextDocument: VersionedTextDocumentIdentifier{
			URI:     d.uri,
			Version: int32(d.version),
		},
		ContentChanges: cc,
	}
}

func (d *Document) desync(why string) error {
	d.desynced = true
	return fmt.Errorf("%w: %s", ErrDesynced, why)
}

// ---- mirror editing ----

func (d *Document) valid(p Pos) bool {
	return p.Row >= 0 && p.Row < len(d.lines) &&
		p.Col >= 0 && p.Col <= len(d.lines[p.Row])
}

func posLess(a, b Pos) bool {
	return a.Row < b.Row || (a.Row == b.Row && a.Col < b.Col)
}

// insert splices rows in at p. The buffers it stores are always fresh, so
// the mirror never aliases memory owned by the caller.
func (d *Document) insert(p Pos, rows [][]byte) {
	line := d.lines[p.Row]
	before, after := line[:p.Col], line[p.Col:]

	if len(rows) == 1 {
		d.lines[p.Row] = concat(before, rows[0], after)
		return
	}

	last := len(rows) - 1
	extra := make([][]byte, 0, last)
	for _, r := range rows[1:last] {
		extra = append(extra, slices.Clone(r))
	}
	extra = append(extra, concat(rows[last], after))

	d.lines[p.Row] = concat(before, rows[0])
	d.lines = slices.Insert(d.lines, p.Row+1, extra...)
}

// remove deletes [start, end).
func (d *Document) remove(start, end Pos) {
	joined := concat(d.lines[start.Row][:start.Col], d.lines[end.Row][end.Col:])
	d.lines[start.Row] = joined
	d.lines = slices.Delete(d.lines, start.Row+1, end.Row+1)
}

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func cloneLines(rows [][]byte) [][]byte {
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = slices.Clone(r)
	}
	if len(out) == 0 {
		out = [][]byte{{}} // a buffer always has at least one (empty) row
	}
	return out
}

// ---- sending ----

// DidOpen sends textDocument/didOpen for d.
func (c *Client) DidOpen(d *Document) error {
	return c.conn.Notify("textDocument/didOpen", d.OpenParams())
}

// DidChange sends textDocument/didChange. A nil p is a no-op, so the result
// of Document.Apply can be passed straight through.
func (c *Client) DidChange(p *DidChangeParams) error {
	if p == nil {
		return nil
	}
	return c.conn.Notify("textDocument/didChange", p)
}

// DidSave sends textDocument/didSave unless the server did not ask for it.
func (c *Client) DidSave(d *Document) error {
	p, ok := d.SaveParams()
	if !ok {
		return nil
	}
	return c.conn.Notify("textDocument/didSave", p)
}

// DidClose sends textDocument/didClose for d.
func (c *Client) DidClose(d *Document) error {
	return c.conn.Notify("textDocument/didClose", d.CloseParams())
}
