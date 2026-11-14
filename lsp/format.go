package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

// textDocument/formatting.
//
// The server answers with edits for the text it was sent. They are converted
// with the mirror, and only if the document is still at the version the
// request was made for: formatting edits applied to text that has since
// changed would damage it, so a result that came too late is an error
// (ErrStale), not a guess.

// FormatOptions are the user's formatting preferences, passed to the server.
// Many servers (gopls) ignore them and use their own style.
type FormatOptions struct {
	TabSize      int
	InsertSpaces bool
}

// FormatResult is what formatting asks the editor to do.
type FormatResult struct {
	// Edits in ge coordinates, valid for the buffer at Version. They do not
	// overlap. There are none if the text is already formatted.
	Edits   []TextEdit
	Version uint64
}

type formattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Options      formattingOptions      `json:"options"`
}

type formattingOptions struct {
	TabSize      int  `json:"tabSize"`
	InsertSpaces bool `json:"insertSpaces"`
}

// Format asks the server to format the whole document. done receives the
// edits on the UI goroutine, later.
func (m *Manager) Format(buf Buffer, opts FormatOptions, done func(FormatResult, error)) {
	s := m.session(buf)
	if s == nil {
		done(FormatResult{}, ErrNotOpen)
		return
	}
	version := buf.Version() // what the user is looking at now
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.format(doc, client, opts, version, done)
	}})
}

func (s *session) format(doc *Document, client *Client, opts FormatOptions, version uint64, done func(FormatResult, error)) {
	fail := func(err error) { s.m.opts.Dispatch(func() { done(FormatResult{}, err) }) }

	switch {
	case doc.desynced:
		fail(ErrDesynced)
		return
	case !client.Capabilities().HasFormatting():
		fail(ErrNotSupported)
		return
	case doc.Version() != version:
		fail(ErrStale) // typed between the command and now
		return
	}

	sent := doc.Version()
	params := formattingParams{
		TextDocument: TextDocumentIdentifier{URI: doc.uri},
		Options:      formattingOptions{TabSize: opts.TabSize, InsertSpaces: opts.InsertSpaces},
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		var raw json.RawMessage
		if err := client.Conn().Call(ctx, "textDocument/formatting", params, &raw); err != nil {
			fail(err)
			return
		}
		wire, err := parseTextEdits(raw)
		if err != nil {
			fail(err)
			return
		}

		s.q.push(event{run: func(doc *Document, _ *Client) {
			res, err := formatResult(doc, sent, wire)
			s.m.opts.Dispatch(func() { done(res, err) })
		}})
	}()
}

// formatResult converts the edits with the mirror, which must still be at the
// version the request was sent for.
func formatResult(doc *Document, sent uint64, wire []wireTextEdit) (FormatResult, error) {
	if doc.Version() != sent {
		return FormatResult{}, ErrStale
	}

	res := FormatResult{Version: sent}
	for _, w := range wire {
		if w.Range == nil {
			continue
		}
		res.Edits = append(res.Edits, TextEdit{
			Start:   doc.FromLSP(w.Range.Start),
			End:     doc.FromLSP(w.Range.End),
			NewText: w.NewText,
		})
	}
	return res, nil
}

// parseTextEdits decodes TextEdit[] | null.
func parseTextEdits(raw json.RawMessage) ([]wireTextEdit, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var edits []wireTextEdit
	if err := json.Unmarshal(raw, &edits); err != nil {
		return nil, errors.Join(errors.New("lsp: decoding text edits"), err)
	}
	return edits, nil
}
