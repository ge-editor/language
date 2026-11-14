package lsp

// textDocument/references.

type referenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      referenceContext       `json:"context"`
}

type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// References asks for the places that use the symbol at pos, including its
// declaration. done receives the locations (each with the text of its line,
// for a list) on the UI goroutine, later.
func (m *Manager) References(buf Buffer, pos Pos, done func(locs []Location, err error)) {
	s := m.session(buf)
	if s == nil {
		done(nil, ErrNotOpen)
		return
	}
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.references(doc, client, pos, done)
	}})
}

func (s *session) references(doc *Document, client *Client, pos Pos, done func([]Location, error)) {
	fail := func(err error) { s.m.opts.Dispatch(func() { done(nil, err) }) }

	base, ok := s.prepare(doc, client, pos, client.Capabilities().HasReferences(), fail)
	if !ok {
		return
	}
	params := referenceParams{
		TextDocument: base.TextDocument,
		Position:     base.Position,
		Context:      referenceContext{IncludeDeclaration: true},
	}
	s.locationRequest(client, "textDocument/references", params, fail, done)
}
