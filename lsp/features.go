package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Request features: hover and go-to-definition.
//
// Both are asked of the session of the document, not of the Client
// directly, for two reasons:
//
//   - The position of the cursor must be converted with the mirror of the
//     text the server has actually been sent. A request is therefore queued
//     behind the pending changes: the session goroutine sends those first
//     and then converts the position, so the server sees the edits before
//     the question about them.
//   - Locations in the answer are converted back with the same mirror.
//
// The request itself is sent from a goroutine of its own, so a slow server
// never holds up the delivery of edits to it. Callbacks run on the UI
// goroutine.

const requestTimeout = 10 * time.Second

var (
	// ErrNotOpen: the buffer is not open in this Manager.
	ErrNotOpen = errors.New("lsp: buffer is not open in a language server")
	// ErrNotSupported: the server does not provide the feature.
	ErrNotSupported = errors.New("lsp: the language server does not support this")
)

// Location is a place in a file, in ge coordinates.
type Location struct {
	Path  string
	Start Pos

	// Text is the line the location is on, trimmed and shortened, for
	// showing in a list. Empty if the line could not be read.
	Text string
}

type textDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Opened reports whether buf is currently synchronized by m. UI goroutine.
func (m *Manager) Opened(buf Buffer) bool {
	s, ok := m.sessions[buf.Key()]
	return ok && !s.closed
}

// Hover asks for the documentation at pos. done receives the text (empty if
// the server has nothing there) on the UI goroutine, later; it is also
// called with an error when the request cannot be made or fails.
func (m *Manager) Hover(buf Buffer, pos Pos, done func(text string, err error)) {
	s := m.session(buf)
	if s == nil {
		done("", ErrNotOpen)
		return
	}
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.hover(doc, client, pos, done)
	}})
}

// Definition asks where the symbol at pos is defined. done receives the
// locations (none if the server found nothing) on the UI goroutine, later.
func (m *Manager) Definition(buf Buffer, pos Pos, done func(locs []Location, err error)) {
	s := m.session(buf)
	if s == nil {
		done(nil, ErrNotOpen)
		return
	}
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.definition(doc, client, pos, done)
	}})
}

func (m *Manager) session(buf Buffer) *session {
	if s, ok := m.sessions[buf.Key()]; ok && !s.closed {
		return s
	}
	return nil
}

// prepare checks what every request needs and builds its parameters. It
// runs on the session goroutine. On failure it delivers err through fail.
func (s *session) prepare(doc *Document, client *Client, pos Pos, supported bool, fail func(error)) (textDocumentPositionParams, bool) {
	switch {
	case doc.desynced:
		fail(ErrDesynced)
	case !supported:
		fail(ErrNotSupported)
	default:
		return textDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: doc.uri},
			Position:     doc.ToLSP(pos),
		}, true
	}
	return textDocumentPositionParams{}, false
}

func (s *session) hover(doc *Document, client *Client, pos Pos, done func(string, error)) {
	fail := func(err error) { s.m.opts.Dispatch(func() { done("", err) }) }

	params, ok := s.prepare(doc, client, pos, client.Capabilities().HasHover(), fail)
	if !ok {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		var res *hoverResponse // a JSON null leaves it nil
		if err := client.Conn().Call(ctx, "textDocument/hover", params, &res); err != nil {
			fail(err)
			return
		}

		text := ""
		if res != nil {
			text = res.plainText()
		}
		s.m.opts.Dispatch(func() { done(text, nil) })
	}()
}

func (s *session) definition(doc *Document, client *Client, pos Pos, done func([]Location, error)) {
	fail := func(err error) { s.m.opts.Dispatch(func() { done(nil, err) }) }

	params, ok := s.prepare(doc, client, pos, client.Capabilities().HasDefinition(), fail)
	if !ok {
		return
	}

	s.locationRequest(client, "textDocument/definition", params, fail, done)
}

// locationRequest sends a request whose answer is a list of locations
// (definition, references) from a goroutine of its own, and converts the
// answer with the mirror on the session goroutine.
func (s *session) locationRequest(client *Client, method string, params any, fail func(error), done func([]Location, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		var raw json.RawMessage
		if err := client.Conn().Call(ctx, method, params, &raw); err != nil {
			fail(err)
			return
		}
		wire, err := parseLocations(raw)
		if err != nil {
			fail(err)
			return
		}

		// Converting needs the mirror, which only the session goroutine
		// may touch: hand the answer back to it.
		s.q.push(event{run: func(doc *Document, _ *Client) {
			locs := convertLocations(doc, wire)
			s.m.opts.Dispatch(func() { done(locs, nil) })
		}})
	}()
}

// ---- hover contents ----

type hoverResponse struct {
	// MarkupContent | MarkedString | MarkedString[]
	Contents json.RawMessage `json:"contents"`
}

// plainText renders the hover contents as plain text: Markdown is kept as
// it is, except that the lines of code fences are dropped, which would only
// be noise in an echo line.
func (h *hoverResponse) plainText() string {
	var parts []string

	add := func(raw json.RawMessage) {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			parts = append(parts, s)
			return
		}
		// MarkupContent {kind, value} or MarkedString {language, value}.
		var v struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(raw, &v) == nil {
			parts = append(parts, v.Value)
		}
	}

	raw := bytes.TrimSpace(h.Contents)
	if len(raw) > 0 && raw[0] == '[' {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) == nil {
			for _, item := range list {
				add(item)
			}
		}
	} else if len(raw) > 0 {
		add(raw)
	}

	var lines []string
	for _, p := range parts {
		for _, line := range strings.Split(p, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				continue
			}
			lines = append(lines, strings.TrimRight(line, " \t\r"))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ---- definition locations ----

// wireLocation covers Location and LocationLink.
type wireLocation struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`

	TargetURI            string `json:"targetUri"`
	TargetRange          *Range `json:"targetRange"`
	TargetSelectionRange *Range `json:"targetSelectionRange"`
}

// parseLocations decodes the result of textDocument/definition:
// Location | Location[] | LocationLink[] | null.
func parseLocations(raw json.RawMessage) ([]wireLocation, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var list []wireLocation
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
	} else {
		var one wireLocation
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		list = []wireLocation{one}
	}

	for i := range list {
		w := &list[i]
		if w.TargetURI == "" {
			continue
		}
		// LocationLink: the selection range is the symbol's name, which is
		// where the cursor should land.
		w.URI = w.TargetURI
		switch {
		case w.TargetSelectionRange != nil:
			w.Range = *w.TargetSelectionRange
		case w.TargetRange != nil:
			w.Range = *w.TargetRange
		}
	}
	return list, nil
}

// convertLocations maps wire locations to ge coordinates. A location in
// the document itself uses the mirror; one in another file is converted
// with that file's line as it is on disk. Locations that are not files are
// skipped.
func convertLocations(doc *Document, list []wireLocation) []Location {
	c := newLocationConverter(doc)
	out := make([]Location, 0, len(list))
	for _, w := range list {
		if loc, ok := c.convert(w); ok {
			out = append(out, loc)
		}
	}
	return out
}

// locationConverter converts wire locations to ge coordinates for one
// answer. It reads each other file once, however many locations point into it.
type locationConverter struct {
	doc   *Document
	files map[string][]byte
}

func newLocationConverter(doc *Document) *locationConverter {
	return &locationConverter{doc: doc, files: make(map[string][]byte)}
}

// convert returns false for a location that is not a file.
func (c *locationConverter) convert(w wireLocation) (Location, bool) {
	path, err := URIToPath(w.URI)
	if err != nil {
		return Location{}, false
	}

	if w.URI == c.doc.uri {
		start := c.doc.FromLSP(w.Range.Start)
		loc := Location{Path: path, Start: start}
		if start.Row >= 0 && start.Row < len(c.doc.lines) {
			loc.Text = shortLine(c.doc.lines[start.Row])
		}
		return loc, true
	}

	data, seen := c.files[path]
	if !seen {
		data, _ = os.ReadFile(path) // nil if unreadable
		c.files[path] = data
	}

	loc := Location{Path: path}
	if data == nil {
		loc.Start = Pos{Row: int(w.Range.Start.Line), Col: int(w.Range.Start.Character)}
	} else {
		loc.Start = columnIn(data, w.Range.Start, c.doc.enc)
		loc.Text = shortLine(lineAt(data, loc.Start.Row))
	}
	return loc, true
}

// maxLocationText bounds the line text kept for a list.
const maxLocationText = 160

// shortLine trims a line for showing in a list.
func shortLine(line []byte) string {
	s := strings.TrimSpace(string(line))
	if len(s) > maxLocationText {
		s = s[:maxLocationText]
		for len(s) > 0 && !utf8.ValidString(s) { // do not cut a character in half
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// lineAt returns line number row (0-based) of data without its line ending,
// or nil past the last line.
func lineAt(data []byte, row int) []byte {
	rest := data
	for n := 0; n < row; n++ {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return nil
		}
		rest = rest[i+1:]
	}
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	return bytes.TrimRight(rest, "\r")
}

// fileColumn converts an LSP position in a file that is not open to ge
// coordinates by reading the line from disk. If the file cannot be read,
// the character offset is used as it is (right for ASCII and utf-8).
func fileColumn(path string, p Position, enc string) Pos {
	fallback := Pos{Row: int(p.Line), Col: int(p.Character)}
	if enc == PositionEncodingUTF8 {
		return fallback
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return columnIn(data, p, enc)
}

// columnIn converts an LSP position in the file content data to ge
// coordinates. A position past the end of a line is the end of that line,
// one past the last line is the end of the file.
func columnIn(data []byte, p Position, enc string) Pos {
	rest := data
	for n := uint32(0); n < p.Line; n++ {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			// Past the last line: the end of the file.
			return Pos{Row: int(n), Col: len(bytes.TrimRight(rest, "\r"))}
		}
		rest = rest[i+1:]
	}
	line := rest
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = bytes.TrimRight(line, "\r")

	return Pos{Row: int(p.Line), Col: decodeColumn(line, p.Character, enc)}
}
