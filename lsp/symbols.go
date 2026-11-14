package lsp

import (
	"bytes"
	"context"
	"encoding/json"
)

// textDocument/documentSymbol and workspace/symbol.
//
// Both return a list of named things with places. Document symbols come as a
// tree (DocumentSymbol) or a flat list (SymbolInformation); the tree is
// flattened in document order with its depth, which is what an outline shows.

// maxWorkspaceSymbols bounds the result of a workspace query: an empty query
// may match every symbol of a large project.
const maxWorkspaceSymbols = 500

// Symbol is a named thing at a place.
type Symbol struct {
	Name      string
	Kind      string // "func", "struct", ... or ""
	Container string // the enclosing symbol or package, if known
	Depth     int    // nesting in a document outline; 0 in a flat list
	Location  Location
}

// DocumentSymbols asks for the outline of the document. done receives the
// symbols in document order on the UI goroutine, later.
func (m *Manager) DocumentSymbols(buf Buffer, done func([]Symbol, error)) {
	s := m.session(buf)
	if s == nil {
		done(nil, ErrNotOpen)
		return
	}
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.documentSymbols(doc, client, done)
	}})
}

// WorkspaceSymbols asks the server of buf for the symbols of the whole
// workspace that match query (the server decides what matches).
func (m *Manager) WorkspaceSymbols(buf Buffer, query string, done func([]Symbol, error)) {
	s := m.session(buf)
	if s == nil {
		done(nil, ErrNotOpen)
		return
	}
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.workspaceSymbols(doc, client, query, done)
	}})
}

func (s *session) documentSymbols(doc *Document, client *Client, done func([]Symbol, error)) {
	fail := func(err error) { s.m.opts.Dispatch(func() { done(nil, err) }) }

	switch {
	case doc.desynced:
		fail(ErrDesynced)
		return
	case !client.Capabilities().HasDocumentSymbol():
		fail(ErrNotSupported)
		return
	}
	params := struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
	}{TextDocumentIdentifier{URI: doc.uri}}

	s.symbolRequest(client, "textDocument/documentSymbol", params, 0, fail, done)
}

func (s *session) workspaceSymbols(doc *Document, client *Client, query string, done func([]Symbol, error)) {
	fail := func(err error) { s.m.opts.Dispatch(func() { done(nil, err) }) }

	switch {
	case doc.desynced:
		fail(ErrDesynced)
		return
	case !client.Capabilities().HasWorkspaceSymbol():
		fail(ErrNotSupported)
		return
	}
	params := struct {
		Query string `json:"query"`
	}{query}

	s.symbolRequest(client, "workspace/symbol", params, maxWorkspaceSymbols, fail, done)
}

// symbolRequest sends the request from a goroutine of its own and converts
// the answer on the session goroutine, which owns the mirror.
func (s *session) symbolRequest(client *Client, method string, params any, limit int, fail func(error), done func([]Symbol, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		var raw json.RawMessage
		if err := client.Conn().Call(ctx, method, params, &raw); err != nil {
			fail(err)
			return
		}
		flat, err := parseSymbols(raw)
		if err != nil {
			fail(err)
			return
		}
		if limit > 0 && len(flat) > limit {
			flat = flat[:limit]
		}

		s.q.push(event{run: func(doc *Document, _ *Client) {
			symbols := convertSymbols(doc, flat)
			s.m.opts.Dispatch(func() { done(symbols, nil) })
		}})
	}()
}

// ---- wire format ----

// wireSymbol covers DocumentSymbol, SymbolInformation and WorkspaceSymbol.
type wireSymbol struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	ContainerName  string `json:"containerName"`
	Range          *Range `json:"range"`
	SelectionRange *Range `json:"selectionRange"`
	Location       *struct {
		URI   string `json:"uri"`
		Range *Range `json:"range"` // WorkspaceSymbol may omit it
	} `json:"location"`
	Children []wireSymbol `json:"children"`
}

// flatSymbol is a symbol with its place as an LSP position, before the
// conversion to ge coordinates.
type flatSymbol struct {
	Name      string
	Kind      int
	Container string
	Depth     int
	URI       string // "" means the document itself
	Pos       Position
}

// parseSymbols decodes DocumentSymbol[] | SymbolInformation[] |
// WorkspaceSymbol[] | null and flattens a tree depth first.
func parseSymbols(raw json.RawMessage) ([]flatSymbol, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var list []wireSymbol
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}

	var out []flatSymbol
	flattenSymbols(&out, list, 0, "")
	return out, nil
}

func flattenSymbols(out *[]flatSymbol, list []wireSymbol, depth int, parent string) {
	for _, w := range list {
		f := flatSymbol{Name: w.Name, Kind: w.Kind, Container: w.ContainerName, Depth: depth}
		if f.Container == "" {
			f.Container = parent
		}

		switch {
		case w.Location != nil: // SymbolInformation / WorkspaceSymbol
			f.URI = w.Location.URI
			if w.Location.Range != nil {
				f.Pos = w.Location.Range.Start
			}
		case w.SelectionRange != nil: // DocumentSymbol: the name, where the cursor should land
			f.Pos = w.SelectionRange.Start
		case w.Range != nil:
			f.Pos = w.Range.Start
		}

		*out = append(*out, f)
		flattenSymbols(out, w.Children, depth+1, w.Name)
	}
}

// convertSymbols maps flat symbols to ge coordinates with the mirror (the
// document's own symbols) or with the files on disk (other files).
func convertSymbols(doc *Document, flat []flatSymbol) []Symbol {
	conv := newLocationConverter(doc)

	out := make([]Symbol, 0, len(flat))
	for _, f := range flat {
		uri := f.URI
		if uri == "" {
			uri = doc.uri
		}
		loc, ok := conv.convert(wireLocation{URI: uri, Range: Range{Start: f.Pos, End: f.Pos}})
		if !ok {
			continue
		}
		out = append(out, Symbol{
			Name:      f.Name,
			Kind:      symbolKindName(f.Kind),
			Container: f.Container,
			Depth:     f.Depth,
			Location:  loc,
		})
	}
	return out
}

var symbolKinds = map[int]string{
	1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class", 6: "method",
	7: "property", 8: "field", 9: "constructor", 10: "enum", 11: "interface",
	12: "func", 13: "var", 14: "const", 15: "string", 16: "number", 17: "bool",
	18: "array", 19: "object", 20: "key", 21: "null", 22: "enum member",
	23: "struct", 24: "event", 25: "operator", 26: "type param",
}

func symbolKindName(kind int) string {
	return symbolKinds[kind]
}
