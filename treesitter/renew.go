//go:build cts

package treesitter

import (
	"os"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// useResetAfterCancel brings back the old behaviour (Parser.Reset after a
// cancelled parse) for A/B comparison. GE_TS_RESET=1
var useResetAfterCancel = os.Getenv("GE_TS_RESET") != ""

// recoverAfterCancel returns the parser to use after a parse was cancelled
// (ParseWithOptions returned nil), and must be called with the parser that
// was cancelled: p = recoverAfterCancel(p, lang, "name").
//
// Calling Parser.Reset() on a cancelled parser is NOT enough: with
// go-tree-sitter v0.25.0 the next parse on the same parser can abort the
// whole process in the C runtime:
//
//	parser.c: ts_parser_parse: Assertion `self->finished_tree.ptr' failed.
//
// This was reproduced with the Markdown parser while typing a lot of
// Japanese text (14/20 runs crashed; 0/20 when the parser was replaced after
// every cancelled parse). The cause inside the C runtime / bindings is not
// pinned down. The Go parser uses the same code path and may be affected too.
//
// Trees do not depend on the parser that produced them, so the parser's
// current tree stays valid and is reused by the next parse.
func recoverAfterCancel(p *ts.Parser, lang *ts.Language, name string) *ts.Parser {
	if useResetAfterCancel {
		p.Reset()
		tracef("parser reset after cancelled parse (%s)", name)
		return p
	}

	p.Close()
	np := ts.NewParser()
	if err := np.SetLanguage(lang); err != nil {
		// The same language was accepted when the parser was first created.
		panic("treesitter: SetLanguage(" + name + ") failed: " + err.Error())
	}
	tracef("parser renewed after cancelled parse (%s)", name)
	return np
}
