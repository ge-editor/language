//go:build cts

package treesitter

import (
	"fmt"
	"os"
	"time"
)

// Debug knobs for the markdown crash investigation. All are off by default
// and are read once at startup:
//
//	GE_TS_TRACE=1     Log every markdown parse to /tmp/ge-ts-trace.log, and
//	                  write the input of the parse that is about to start to
//	                  /tmp/ge-md-last.md. Both are written with plain
//	                  write(2) calls, so they survive a SIGABRT. If the last
//	                  "parse start" line has no matching "parse done", that
//	                  parse is the one that crashed, and ge-md-last.md is its
//	                  input.
//	GE_TS_NOCB=1      Parse with Parser.Parse: no ProgressCallback and no
//	                  read callback, so a parse can never be cancelled and
//	                  no Go callback is ever invoked from C.
//	GE_TS_NOOLD=1     Never reuse the old tree (always parse from scratch).
//
// After a cancelled parse the parser is replaced (renew.go); GE_TS_RESET=1
// restores the old Parser.Reset() behaviour for A/B comparison.
//
// The worker (editorleaf/syntax) has its own knobs, see worker_debug.go.
// GE_TS_NOCANCEL there only stops the worker from cancelling; with
// GE_TS_NOCANCEL alone the ProgressCallback is still installed (and never
// asks to stop). Combine the two to remove both.
var (
	dbgTrace    = os.Getenv("GE_TS_TRACE") != ""
	dbgNoCB     = os.Getenv("GE_TS_NOCB") != ""
	dbgNoOld    = os.Getenv("GE_TS_NOOLD") != ""
)

const (
	dbgTraceLog  = "/tmp/ge-ts-trace.log"
	dbgLastInput = "/tmp/ge-md-last.md"
)

func tracef(format string, args ...any) {
	if !dbgTrace {
		return
	}
	f, err := os.OpenFile(dbgTraceLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000000"), fmt.Sprintf(format, args...))
}

func dumpInput(src []byte) {
	if !dbgTrace {
		return
	}
	_ = os.WriteFile(dbgLastInput, src, 0o644)
}
