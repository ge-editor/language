//go:build cts

// Package treesitter implements editorleaf/syntax.Parser for Go, using
// the official C tree-sitter runtime through cgo
// (github.com/tree-sitter/go-tree-sitter) and the Go grammar
// (github.com/tree-sitter/tree-sitter-go).
//
// This file only builds with `-tags cts`, and only this package (plus its
// two direct dependencies above) requires cgo / a C compiler. Nothing
// else in ge does. See ge/register_treesitter.go for how it gets linked
// into the default-off, opt-in build.
//
// # API surface confirmed / still unverified against your installed version
//
// Confirmed against pkg.go.dev for github.com/tree-sitter/go-tree-sitter
// v0.25.0 (the version in language/go.sum as of this writing):
//
//   - Parser.ParseWithOptions(callback func(int, Point) []byte,
//     oldTree *Tree, options *ParseOptions) *Tree -- note it takes a
//     read callback, not src directly, and options is a pointer.
//     Parser.Parse(text, oldTree) is the plain byte-slice method but has
//     no cancellation hook; ParseCtx is deprecated in favor of
//     ParseWithOptions.
//   - ParseOptions.ProgressCallback is func(ParseState) bool (value,
//     not *ParseState).
//
// Not directly confirmed (best-effort from docs; a compile error here is
// the same quick fix as above):
//
//   - ts.Tree.Edit takes *ts.InputEdit (a pointer) -- confirm value vs
//     pointer.
//   - Node.Kind() -- some versions name this Type() instead
//     (Kind is the current upstream name as of this writing).
//   - TreeCursor field/method names for the manual walk below
//     (GotoFirstChild / GotoNextSibling / GotoParent). These mirror the
//     C API closely and are very unlikely to differ, but were not
//     directly confirmed here.
//
// Run `go build -tags cts ./...`; every mismatch above is a compile
// error, not a silent bug.
package treesitter

import (
	"context"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/ge-editor/editorleaf/editbuffer"
	"github.com/ge-editor/editorleaf/highlight"
	"github.com/ge-editor/editorleaf/syntax"
	"github.com/ge-editor/theme"
)

func init() {
	syntax.Register("go", NewGoParser)
}

// NewGoParser is a syntax.Factory for Go.
func NewGoParser() syntax.Parser {
	lang := ts.NewLanguage(tsgo.Language())
	p := ts.NewParser()
	if err := p.SetLanguage(lang); err != nil {
		// The grammar is fixed and known-good at compile time, so this
		// can only fail from a packaging mistake (e.g. ABI mismatch
		// between go-tree-sitter and tree-sitter-go). There is no
		// sensible per-call fallback, so fail loudly instead of
		// silently returning a parser that will never produce a tree.
		panic("treesitter: SetLanguage(go) failed: " + err.Error())
	}
	return &goParser{lang: lang, parser: p}
}

type goParser struct {
	lang   *ts.Language
	parser *ts.Parser
	tree   *ts.Tree // nil until the first Parse
	spans  []highlight.Span
}

func (g *goParser) Parse(ctx context.Context, src []byte) {
	// gelog.Debug("[Parse]")
	g.replaceTree(g.parseWithCancel(ctx, src, nil))
	if ctx.Err() != nil {
		return
	}
	spans := g.buildSpans(ctx)
	if ctx.Err() != nil {
		return
	}
	g.spans = spans
}

func (g *goParser) Edit(ctx context.Context, e syntax.Edit, src []byte) {
	// gelog.Debug("[Edit] 1")
	if g.tree == nil {
		g.Parse(ctx, src)
		return
	}
	// gelog.Debug("[Edit] 2")

	g.tree.Edit(&ts.InputEdit{
		StartByte:      uint(e.StartByte),
		OldEndByte:     uint(e.OldEndByte),
		NewEndByte:     uint(e.NewEndByte),
		StartPosition:  ts.Point{Row: uint(e.StartRow), Column: uint(e.StartCol)},
		OldEndPosition: ts.Point{Row: uint(e.OldEndRow), Column: uint(e.OldEndCol)},
		NewEndPosition: ts.Point{Row: uint(e.NewEndRow), Column: uint(e.NewEndCol)},
	})

	g.replaceTree(g.parseWithCancel(ctx, src, g.tree))
	if ctx.Err() != nil {
		return
	}
	spans := g.buildSpans(ctx)
	if ctx.Err() != nil {
		return
	}
	g.spans = spans
}

func (g *goParser) Spans() []highlight.Span { return g.spans }

func (g *goParser) Close() {
	g.replaceTree(nil)
}

// replaceTree installs next as g.tree, closing whatever was there before.
// old is passed to parseWithCancel as the base for incremental parsing,
// so it must still be valid when this is called -- do not Close it
// yourself first.
func (g *goParser) replaceTree(next *ts.Tree) {
	if g.tree != nil && g.tree != next {
		g.tree.Close()
	}
	g.tree = next
}

// parseWithCancel parses src, aborting early if ctx is done before the
// parse finishes.
//
// Confirmed against github.com/tree-sitter/go-tree-sitter v0.25.0
// (pkg.go.dev): ParseWithOptions takes a read callback
// (func(int, Point) []byte), not a plain []byte -- Parse(text, oldTree)
// is the simple, non-cancellable byte-slice method; cancellation is only
// available through ParseWithOptions + ParseOptions.ProgressCallback,
// which takes options by pointer and receives ParseState by value
// (not *ParseState).
func (g *goParser) parseWithCancel(ctx context.Context, src []byte, old *ts.Tree) *ts.Tree {
	// tree-sitter reads text through this callback instead of taking
	// src directly; returning the remainder of src from offset each
	// time is correct (tree-sitter takes what it needs and calls again
	// for more), if not maximally efficient. offset is normally 0 when
	// there is no old tree and otherwise starts near the edited region.
	read := func(offset int, _ ts.Point) []byte {
		if offset < 0 || offset >= len(src) {
			return nil
		}
		return src[offset:]
	}

	t := g.parser.ParseWithOptions(read, old, &ts.ParseOptions{
		ProgressCallback: func(_ ts.ParseState) bool {
			select {
			case <-ctx.Done():
				return true // ask tree-sitter to stop
			default:
				return false
			}
		},
	})
	if t == nil {
		// Either genuinely failed, or aborted via ProgressCallback. In
		// both cases keep whatever tree we already have (old) rather
		// than losing highlighting entirely; the next edit will retry.
		return old
	}
	return t
}

// buildSpans walks the whole tree and maps each named node's Kind() to a
// color via theme.CodeColors, then resolves the result into the sorted,
// non-overlapping form editorleaf/syntax.Parser promises its caller.
//
// This mirrors the approach in documents/treesitter.md (color by grammar
// node type) rather than a tree-sitter highlights query, since
// theme.CodeColors is already keyed by node type
// (e.g. "interpreted_string_literal", "comment", "identifier").
//
// Walking the whole tree on every keystroke is the simplest correct
// thing and is what this skeleton does; if it shows up in profiles on
// large files, restrict the walk to the currently visible row range
// first (ts.TreeCursor supports seeking to a byte/point range in recent
// versions) before optimizing further.
func (g *goParser) buildSpans(ctx context.Context) []highlight.Span {
	// gelog.Debug("buildSpans")
	if g.tree == nil {
		return nil
	}

	// gelog.Debug("buildSpans 2")
	var raw []highlight.Span
	cursor := g.tree.RootNode().Walk()
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
			Start: editbuffer.RowsPos{RowIndex: int(sp.Row), ColIndex: int(sp.Column)},
			End:   editbuffer.RowsPos{RowIndex: int(ep.Row), ColIndex: int(ep.Column)},
			Color: style,
			// Without this, editorleaf.go's drawEditorleaf renders a
			// span using the zero-value tcell.Style (no color) whenever
			// the cursor sits inside it (see FindHighlightSpan's
			// isOnCursor / span.ColorIfActive branch) -- i.e. whatever
			// token you're actively editing would always look
			// unhighlighted. Use the same color so editing a token
			// doesn't visually "turn off" its highlighting.
			ColorIfActive: style,
		})
	})
	if ctx.Err() != nil {
		return nil
	}

	return flattenSpans(raw)
}
