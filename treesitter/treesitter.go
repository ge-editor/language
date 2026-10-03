package treesitter

import (
	"context"
	"sort"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/ge-editor/editorleaf/editbuffer"
	"github.com/ge-editor/editorleaf/highlight"
)

// walk performs a depth-first, pre-order traversal of the tree rooted at
// cursor's current node, calling visit for every node (named or not;
// visit itself filters).
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

// flattenSpans sorts spans by Start and resolves overlaps by keeping the
// innermost (last, among those with the same Start, since a child is
// walked after -- no, walked as a *sibling* of -- its colored ancestor;
// see below) span for any given byte range, so the result satisfies the
// "sorted, non-overlapping" contract of editorleaf/syntax.Parser.
//
// theme.CodeColors is keyed by leaf-ish token node types
// (identifier, comment, string literals, ...), so in practice the walk
// above rarely produces overlapping spans in the first place; this pass
// is a safety net, not the common case.
func flattenSpans(spans []highlight.Span) []highlight.Span {
	// gelog.Debug("flattenSpans")
	if len(spans) == 0 {
		return nil
	}

	// gelog.Debug("flattenSpans 2")
	less := func(a, b editbuffer.RowsPos) bool {
		return a.RowIndex < b.RowIndex || (a.RowIndex == b.RowIndex && a.ColIndex < b.ColIndex)
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return less(spans[i].Start, spans[j].Start)
		}
		// Wider span (larger End) first, so a narrower, more specific
		// span with the same start can override it below.
		return less(spans[j].End, spans[i].End)
	})

	out := spans[:0:0] //nolint:gocritic // intentional: build a fresh non-overlapping slice
	for _, s := range spans {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if !less(last.End, s.Start) { // s starts before last ends: overlap
				if !less(s.End, last.End) {
					continue // s does not narrow the overlap; keep last
				}
				last.End = s.Start // truncate last so s can follow without overlapping
				if last.Start == last.End {
					out = out[:n-1] // last became empty; drop it
				}
			}
		}
		out = append(out, s)
	}
	// gelog.Debug("flattenSpans out", out)
	return out
}
