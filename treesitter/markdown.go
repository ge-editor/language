//go:build cts

// go get github.com/tree-sitter-grammars/tree-sitter-markdown@v0.5.1

// Package treesitter implements editorleaf/syntax.Parser for Markdown,
// using the official C tree-sitter runtime through cgo.
//
// The Markdown grammar is provided by
// github.com/tree-sitter-grammars/tree-sitter-markdown.
//
// This file only builds with `-tags cts`, matching the other Tree-sitter
// parsers in this package.
package treesitter

import (
	"context"
	"hash/crc32"
	"time"
	"unicode/utf8"

	// tsmd "github.com/tree-sitter-grammars/tree-sitter-markdown/bindings/go"
	tsmd "github.com/Bitspark/tree-sitter-markdown/bindings/go"
	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/ge-editor/editorleaf/editbuffer"
	"github.com/ge-editor/editorleaf/highlight"
	"github.com/ge-editor/editorleaf/syntax"
	"github.com/ge-editor/theme"
)

func init() {
	syntax.Register("markdown", NewMarkdownParser)
}

// NewMarkdownParser is a syntax.Factory for Markdown.
//
// The Markdown grammar is a block grammar. Inline Markdown is represented
// by inline nodes in the block tree, so this parser intentionally follows
// the same single-tree traversal model as the Go parser.
func NewMarkdownParser() syntax.Parser {
	lang := ts.NewLanguage(tsmd.Language())
	p := ts.NewParser()
	if err := p.SetLanguage(lang); err != nil {
		panic("treesitter: SetLanguage(markdown) failed: " + err.Error())
	}
	return &markdownParser{lang: lang, parser: p}
}

type markdownParser struct {
	lang   *ts.Language
	parser *ts.Parser
	tree   *ts.Tree
	spans  []highlight.Span
}

func (m *markdownParser) Parse(ctx context.Context, src []byte) {
	m.replaceTree(m.parseWithCancel(ctx, src, nil))
	if ctx.Err() != nil {
		return
	}

	spans := m.buildSpans(ctx)
	if ctx.Err() != nil {
		return
	}
	m.spans = spans
}

func (m *markdownParser) Edit(ctx context.Context, e syntax.Edit, src []byte) {
	// gelog.Debug("syntax", "Edit", e)
	tracef("edit start=%d old_end=%d new_end=%d row=%d col=%d len(src)=%d",
		e.StartByte, e.OldEndByte, e.NewEndByte, e.StartRow, e.StartCol, len(src))

	if m.tree == nil {
		m.Parse(ctx, src)
		return
	}

	m.tree.Edit(&ts.InputEdit{
		StartByte:      uint(e.StartByte),
		OldEndByte:     uint(e.OldEndByte),
		NewEndByte:     uint(e.NewEndByte),
		StartPosition:  ts.Point{Row: uint(e.StartRow), Column: uint(e.StartCol)},
		OldEndPosition: ts.Point{Row: uint(e.OldEndRow), Column: uint(e.OldEndCol)},
		NewEndPosition: ts.Point{Row: uint(e.NewEndRow), Column: uint(e.NewEndCol)},
	})

	m.replaceTree(m.parseWithCancel(ctx, src, m.tree))
	if ctx.Err() != nil {
		return
	}

	spans := m.buildSpans(ctx)
	if ctx.Err() != nil {
		return
	}
	m.spans = spans
}

func (m *markdownParser) Spans() []highlight.Span {
	return m.spans
}

func (m *markdownParser) Close() {
	m.replaceTree(nil)
}

func (m *markdownParser) replaceTree(next *ts.Tree) {
	if m.tree != nil && m.tree != next {
		m.tree.Close()
	}
	m.tree = next
}

func (m *markdownParser) parseWithCancel(
	ctx context.Context,
	src []byte,
	old *ts.Tree,
) *ts.Tree {
	reuse := old
	if dbgNoOld { // GE_TS_NOOLD
		reuse = nil
	}

	// GE_TS_TRACE: the input is written before the parse starts so that it
	// survives an abort inside the C runtime.
	dumpInput(src)
	tracef("parse start bytes=%d utf8=%t crc=%08x reuse_old=%t nocb=%t",
		len(src), utf8.Valid(src), crc32.ChecksumIEEE(src), reuse != nil, dbgNoCB)
	started := time.Now()

	var t *ts.Tree
	if dbgNoCB { // GE_TS_NOCB: no ProgressCallback, no read callback
		t = m.parser.Parse(src, reuse)
	} else {
		read := func(offset int, _ ts.Point) []byte {
			if offset < 0 || offset >= len(src) {
				return nil
			}
			return src[offset:]
		}

		t = m.parser.ParseWithOptions(read, reuse, &ts.ParseOptions{
			ProgressCallback: func(_ ts.ParseState) bool {
				select {
				case <-ctx.Done():
					return true
				default:
					return false
				}
			},
		})
	}
	tracef("parse done took=%s tree=%t ctx_err=%v", time.Since(started), t != nil, ctx.Err())

	if t == nil {
		// Cancelled (or failed). tree-sitter would resume this parse on the
		// next call, and Reset() is not enough to make the parser safe to
		// reuse (it can abort the process, see recoverAfterCancel). Replace
		// the parser so the next parse starts fresh from the edited old
		// tree and the new source.
		m.parser = recoverAfterCancel(m.parser, m.lang, "markdown")
		return old
	}
	return t
}

// buildSpans walks the Markdown syntax tree and maps node kinds to
// theme.CodeColors. Only node kinds already present in the theme are emitted.
//
// This deliberately does not introduce a Markdown-specific color vocabulary.
// It lets the existing theme decide which Markdown node types should be
// visible, while preserving the same syntax.Parser contract as the Go parser.
func (m *markdownParser) buildSpans(ctx context.Context) []highlight.Span {
	if m.tree == nil {
		return nil
	}

	var raw []highlight.Span
	cursor := m.tree.RootNode().Walk()
	defer cursor.Close()

	walk(ctx, cursor, func(n *ts.Node) {
		if !n.IsNamed() {
			return
		}

		style, ok := theme.CodeColors[n.Kind()]
		if !ok {
			return
		}

		sp, ep := n.StartPosition(), n.EndPosition()
		raw = append(raw, highlight.Span{
			Start: editbuffer.RowsPos{
				RowIndex: int(sp.Row),
				ColIndex: int(sp.Column),
			},
			End: editbuffer.RowsPos{
				RowIndex: int(ep.Row),
				ColIndex: int(ep.Column),
			},
			Color:         style,
			ColorIfActive: style,
		})
	})

	if ctx.Err() != nil {
		return nil
	}

	//  gelog.Debug("markdown buildSpans", "raw", len(raw))
	// return flattenMarkdownSpans(raw)
	return flattenSpans(raw)
	// return raw // 試験的に
}

/*
func walk(ctx context.Context, cursor *ts.TreeCursor, visit func(*ts.Node)) {
	for {
		if ctx.Err() != nil {
			return
		}
		visit(cursor.Node())

		if cursor.GotoFirstChild() {
			continue
		}
		for !cursor.GotoNextSibling() {
			if !cursor.GotoParent() {
				return
			}
		}
	}
}
*/

/*
func flattenMarkdownSpans(spans []highlight.Span) []highlight.Span {
	if len(spans) == 0 {
		return nil
	}

	less := func(a, b rows.RowsPos) bool {
		return a.RowIndex < b.RowIndex ||
			(a.RowIndex == b.RowIndex && a.ColIndex < b.ColIndex)
	}

	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return less(spans[i].Start, spans[j].Start)
		}
		return less(spans[j].End, spans[i].End)
	})

	out := spans[:0:0]
	for _, s := range spans {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if !less(last.End, s.Start) {
				if !less(s.End, last.End) {
					continue
				}
				last.End = s.Start
				if last.Start == last.End {
					out = out[:n-1]
				}
			}
		}
		out = append(out, s)
	}

	return out
}
*/
