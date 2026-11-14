package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// textDocument/completion.
//
// The Manager asks the server and returns the items with every position
// already in ge coordinates, and with a complete edit: the range to replace
// and the text to put there. Choosing among the items and applying the edit
// is the editor's business.
//
// The client announces no snippet support (see defaultClientCapabilities),
// so servers answer with plain text.

// maxCompletionItems bounds what is handed to the UI. A server may return
// thousands of items for an empty prefix; the user narrows them by typing.
const maxCompletionItems = 1000

// TextEdit replaces [Start, End) with NewText, in ge coordinates.
type TextEdit struct {
	Start, End Pos
	NewText    string
}

// CompletionItem is one completion candidate.
type CompletionItem struct {
	Label      string
	Kind       string // "func", "var", ... ("" if the server gave none)
	Detail     string
	FilterText string // what the user's typing is matched against; Label if empty

	// Edit is what accepting the item does to the text. It is always set:
	// when the server gave only a label or insertText, the edit replaces
	// the identifier prefix before the cursor.
	Edit TextEdit

	// Additional are further edits that go with it, typically an added
	// import. Their ranges may be anywhere in the document.
	Additional []TextEdit
}

// Completion asks for completions at pos. done receives the items, in the
// server's order of preference, on the UI goroutine, later; an empty result
// is not an error.
func (m *Manager) Completion(buf Buffer, pos Pos, done func(items []CompletionItem, err error)) {
	s := m.session(buf)
	if s == nil {
		done(nil, ErrNotOpen)
		return
	}
	s.q.push(event{run: func(doc *Document, client *Client) {
		s.completion(doc, client, pos, done)
	}})
}

type completionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      completionContext      `json:"context"`
}

type completionContext struct {
	TriggerKind int `json:"triggerKind"` // 1 = invoked by the user
}

func (s *session) completion(doc *Document, client *Client, pos Pos, done func([]CompletionItem, error)) {
	fail := func(err error) { s.m.opts.Dispatch(func() { done(nil, err) }) }

	base, ok := s.prepare(doc, client, pos, client.Capabilities().HasCompletion(), fail)
	if !ok {
		return
	}
	params := completionParams{
		TextDocument: base.TextDocument,
		Position:     base.Position,
		Context:      completionContext{TriggerKind: 1},
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		var raw json.RawMessage
		if err := client.Conn().Call(ctx, "textDocument/completion", params, &raw); err != nil {
			fail(err)
			return
		}
		wire, err := parseCompletion(raw)
		if err != nil {
			fail(err)
			return
		}

		// The positions in the answer are converted with the mirror, on
		// the goroutine that owns it.
		s.q.push(event{run: func(doc *Document, _ *Client) {
			items := convertCompletion(doc, pos, wire)
			s.m.opts.Dispatch(func() { done(items, nil) })
		}})
	}()
}

// ---- wire format ----

type wireTextEdit struct {
	Range   *Range `json:"range"`
	NewText string `json:"newText"`

	// InsertReplaceEdit
	Insert  *Range `json:"insert"`
	Replace *Range `json:"replace"`
}

type wireCompletionItem struct {
	Label            string          `json:"label"`
	Kind             int             `json:"kind"`
	Detail           string          `json:"detail"`
	SortText         string          `json:"sortText"`
	FilterText       string          `json:"filterText"`
	InsertText       string          `json:"insertText"`
	InsertTextFormat int             `json:"insertTextFormat"` // 1 plain, 2 snippet
	TextEdit         *wireTextEdit   `json:"textEdit"`
	Additional       []wireTextEdit  `json:"additionalTextEdits"`
	Deprecated       json.RawMessage `json:"deprecated"`
}

// parseCompletion decodes the result of textDocument/completion:
// CompletionItem[] | CompletionList | null.
func parseCompletion(raw json.RawMessage) ([]wireCompletionItem, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	if raw[0] == '[' {
		var items []wireCompletionItem
		err := json.Unmarshal(raw, &items)
		return items, err
	}

	var list struct {
		Items []wireCompletionItem `json:"items"`
	}
	err := json.Unmarshal(raw, &list)
	return list.Items, err
}

// convertCompletion maps the wire items to CompletionItem with the mirror
// and orders them by the server's sortText (the label where it gave none).
func convertCompletion(doc *Document, cursor Pos, list []wireCompletionItem) []CompletionItem {
	slices.SortStableFunc(list, func(a, b wireCompletionItem) int {
		return strings.Compare(sortKey(a), sortKey(b))
	})
	if len(list) > maxCompletionItems {
		list = list[:maxCompletionItems]
	}

	// The default range: the identifier being typed, up to the cursor.
	defaultRange := TextEdit{Start: cursor, End: cursor}
	if cursor.Row >= 0 && cursor.Row < doc.NumLines() {
		line := doc.lines[cursor.Row]
		if cursor.Col >= 0 && cursor.Col <= len(line) {
			defaultRange.Start.Col = wordStart(line, cursor.Col)
		}
	}

	out := make([]CompletionItem, 0, len(list))
	for _, w := range list {
		item := CompletionItem{
			Label:      w.Label,
			Kind:       completionKindName(w.Kind),
			Detail:     w.Detail,
			FilterText: w.FilterText,
			Edit:       defaultRange,
		}

		// What to insert: the textEdit, else insertText, else the label. A
		// snippet cannot be inserted as text, so it falls back to the label.
		item.Edit.NewText = w.Label
		if w.InsertText != "" && w.InsertTextFormat != 2 {
			item.Edit.NewText = w.InsertText
		}
		if te := w.TextEdit; te != nil && w.InsertTextFormat != 2 {
			rng := te.Range
			if rng == nil {
				rng = te.Replace // InsertReplaceEdit: replace the whole word
			}
			if rng != nil {
				item.Edit = TextEdit{
					Start:   doc.FromLSP(rng.Start),
					End:     doc.FromLSP(rng.End),
					NewText: te.NewText,
				}
			}
		}

		for _, a := range w.Additional {
			if a.Range == nil {
				continue
			}
			item.Additional = append(item.Additional, TextEdit{
				Start:   doc.FromLSP(a.Range.Start),
				End:     doc.FromLSP(a.Range.End),
				NewText: a.NewText,
			})
		}
		out = append(out, item)
	}
	return out
}

func sortKey(w wireCompletionItem) string {
	if w.SortText != "" {
		return w.SortText
	}
	return w.Label
}

// wordStart returns the byte column where the identifier that ends at col
// begins (col itself if the character before it is not part of one).
func wordStart(line []byte, col int) int {
	i := col
	for i > 0 {
		r, size := utf8.DecodeLastRune(line[:i])
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			break
		}
		i -= size
	}
	return i
}

var completionKinds = map[int]string{
	1: "text", 2: "method", 3: "func", 4: "constructor", 5: "field", 6: "var",
	7: "class", 8: "interface", 9: "module", 10: "property", 11: "unit",
	12: "value", 13: "enum", 14: "keyword", 15: "snippet", 16: "color",
	17: "file", 18: "reference", 19: "folder", 20: "enum member",
	21: "const", 22: "struct", 23: "event", 24: "operator", 25: "type param",
}

func completionKindName(kind int) string {
	return completionKinds[kind]
}
