package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// textDocument/codeAction.
//
// Only actions that carry their edit are offered. An action that is just a
// command (the server runs it and then asks the client to apply an edit), one
// whose edit must be fetched by a later request (codeAction/resolve), a
// disabled one, and one that needs file operations are counted as skipped and
// not offered: the client does not support them, and showing an action that
// cannot be carried out would be worse than not showing it.
//
// Quick fixes depend on the diagnostics at the range, so those are sent in the
// request. Like a rename, an action may edit several files, so the same
// precautions apply: other documents are brought up to date first and the
// buffer versions are checked against the edits.

// CodeAction is an action that can be carried out.
type CodeAction struct {
	Title     string
	Kind      string // "quickfix", "refactor.extract", ... or ""
	Preferred bool   // the server's suggestion for the diagnostic at the range
	Edit      *WorkspaceEdit
}

// CodeActionsResult is the answer to a code action request.
type CodeActionsResult struct {
	// Actions that can be carried out, the preferred ones first.
	Actions []CodeAction

	// Skipped is how many more the server offered that this client cannot
	// carry out (see the package comment of this file).
	Skipped int
}

type codeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      codeActionContext      `json:"context"`
}

type codeActionContext struct {
	Diagnostics []wireDiagnostic `json:"diagnostics"`
}

// CodeActions asks for the actions available for the range [start, end] of the
// buffer (a cursor is a range with start == end). done receives the result on
// the UI goroutine, later.
func (m *Manager) CodeActions(buf Buffer, start, end Pos, done func(CodeActionsResult, error)) {
	s := m.session(buf)
	if s == nil {
		done(CodeActionsResult{}, ErrNotOpen)
		return
	}

	versions, barrier := m.syncPoint(s)
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.codeActions(doc, client, start, end, barrier, versions, done)
	}})
}

func (s *session) codeActions(doc *Document, client *Client, start, end Pos,
	barrier *sync.WaitGroup, versions map[*session]uint64, done func(CodeActionsResult, error)) {

	fail := func(err error) { s.m.opts.Dispatch(func() { done(CodeActionsResult{}, err) }) }

	switch {
	case doc.desynced:
		fail(ErrDesynced)
		return
	case !client.Capabilities().HasCodeAction():
		fail(ErrNotSupported)
		return
	}

	rng := Range{Start: doc.ToLSP(start), End: doc.ToLSP(end)}

	// The diagnostics at the range, if the server's latest ones are for the
	// text as it is now.
	diags := []wireDiagnostic{}
	if s.diagsVersion == doc.Version() {
		for _, d := range s.diags {
			if rangesTouch(d.Range, rng) {
				diags = append(diags, d)
			}
		}
	}

	if !waitTimeout(barrier, barrierTimeout) {
		fail(errors.New("lsp: the other open documents could not be brought up to date in time"))
		return
	}

	params := codeActionParams{
		TextDocument: TextDocumentIdentifier{URI: doc.uri},
		Range:        rng,
		Context:      codeActionContext{Diagnostics: diags},
	}
	enc := doc.enc

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		var raw json.RawMessage
		if err := client.Conn().Call(ctx, "textDocument/codeAction", params, &raw); err != nil {
			fail(err)
			return
		}
		wire, skipped, err := parseCodeActions(raw)
		if err != nil {
			fail(err)
			return
		}

		// Converting the edits needs the buffers, on the UI goroutine.
		s.m.opts.Dispatch(func() {
			res, err := s.m.finishCodeActions(wire, skipped, versions, enc)
			done(res, err)
		})
	}()
}

// rangesTouch reports whether two ranges overlap or touch (a cursor at the end
// of an identifier counts as being on it).
func rangesTouch(a, b Range) bool {
	le := func(p, q Position) bool {
		return p.Line < q.Line || (p.Line == q.Line && p.Character <= q.Character)
	}
	return le(a.Start, b.End) && le(b.Start, a.End)
}

// ---- wire format ----

type wireCodeAction struct {
	Title       string          `json:"title"`
	Kind        string          `json:"kind"`
	IsPreferred bool            `json:"isPreferred"`
	Disabled    *struct{}       `json:"disabled"`
	Edit        json.RawMessage `json:"edit"`
}

// codeActionWire is an action that carries an edit, before conversion.
type codeActionWire struct {
	title, kind string
	preferred   bool
	files       []wireFileEdit
}

// parseCodeActions decodes (Command | CodeAction)[] | null. It returns the
// actions that carry an edit and how many entries were left out.
func parseCodeActions(raw json.RawMessage) ([]codeActionWire, int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, 0, nil
	}

	var list []wireCodeAction
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, 0, fmt.Errorf("lsp: decoding code actions: %w", err)
	}

	var (
		out     []codeActionWire
		skipped int
	)
	for _, a := range list {
		edit := bytes.TrimSpace(a.Edit)
		if a.Disabled != nil || len(edit) == 0 || string(edit) == "null" {
			skipped++ // disabled, a bare command, or an edit to be resolved later
			continue
		}

		files, err := parseWorkspaceEdit(edit)
		if err != nil || len(files) == 0 {
			skipped++ // file operations, or nothing to do
			continue
		}
		out = append(out, codeActionWire{title: a.Title, kind: a.Kind, preferred: a.IsPreferred, files: files})
	}
	return out, skipped, nil
}

// finishCodeActions converts the edits of the actions to ge coordinates and
// checks them against the buffers (UI goroutine). A buffer that changed
// while the request ran makes the whole answer stale; an action whose files
// cannot be read is skipped.
func (m *Manager) finishCodeActions(wire []codeActionWire, skipped int, versions map[*session]uint64, enc string) (CodeActionsResult, error) {
	res := CodeActionsResult{Skipped: skipped}

	for _, a := range wire {
		we, err := m.finishRename(a.files, versions, enc)
		switch {
		case errors.Is(err, ErrStale):
			return CodeActionsResult{}, err
		case err != nil:
			res.Skipped++
			continue
		}
		res.Actions = append(res.Actions, CodeAction{Title: a.title, Kind: a.kind, Preferred: a.preferred, Edit: we})
	}

	// The preferred actions first, the server's order otherwise.
	sort.SliceStable(res.Actions, func(i, j int) bool {
		return res.Actions[i].Preferred && !res.Actions[j].Preferred
	})
	return res, nil
}
