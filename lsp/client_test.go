package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The test binary doubles as a fake language server: StartClient runs
// os.Args[0] with GE_LSP_FAKE_SERVER=<mode>, and TestMain turns that
// process into a server instead of running tests.
//
//	ok    : well-behaved server
//	hang  : never answers shutdown and ignores exit (must be killed)
//	crash : dies on the "boom" request
const fakeServerEnv = "GE_LSP_FAKE_SERVER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeServerEnv); mode != "" {
		runFakeServer(mode)
		return
	}
	os.Exit(m.Run())
}

func runFakeServer(mode string) {
	var shutdownSeen, sawZZ atomic.Bool

	var conn *Conn
	conn = NewConn(os.Stdin, os.Stdout, Handler{
		Request: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case "textDocument/hover":
				return map[string]any{
					"contents": map[string]any{
						"kind":  "markdown",
						"value": "```go\nfunc f()\n```\nDoc text",
					},
				}, nil
			case "textDocument/completion":
				rng := func(l, c1, c2 int) map[string]any {
					return map[string]any{
						"start": map[string]any{"line": l, "character": c1},
						"end":   map[string]any{"line": l, "character": c2},
					}
				}
				return map[string]any{
					"isIncomplete": false,
					"items": []map[string]any{
						{
							"label": "Println", "kind": 3, "detail": "func(a ...any)", "sortText": "b",
							"textEdit":            map[string]any{"range": rng(1, 0, 2), "newText": "Println"},
							"additionalTextEdits": []map[string]any{{"range": rng(0, 0, 0), "newText": "import \"fmt\"\n"}},
						},
						{"label": "Print", "kind": 3, "sortText": "a", "insertText": "Print"},
						{"label": "Snip", "kind": 15, "sortText": "c", "insertText": "Snip(${1:x})", "insertTextFormat": 2},
					},
				}, nil
			case "textDocument/codeAction":
				var p struct {
					TextDocument struct {
						URI string `json:"uri"`
					} `json:"textDocument"`
					Context struct {
						Diagnostics []json.RawMessage `json:"diagnostics"`
					} `json:"context"`
				}
				_ = json.Unmarshal(params, &p)
				edit := map[string]any{"changes": map[string]any{
					p.TextDocument.URI: []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
							"end":   map[string]any{"line": 0, "character": 3},
						},
						"newText": "abc",
					}},
				}}
				return []map[string]any{
					{"title": fmt.Sprintf("Fix A (%d diagnostics)", len(p.Context.Diagnostics)), "kind": "quickfix", "edit": edit},
					{"title": "Preferred", "kind": "refactor", "isPreferred": true, "edit": edit},
					{"title": "Cmd only", "command": "do.it"},
					{"title": "Disabled", "disabled": map[string]any{"reason": "no"}, "edit": edit},
					{"title": "Lazy", "kind": "refactor"},
					{"title": "Files", "edit": map[string]any{"documentChanges": []map[string]any{
						{"kind": "create", "uri": "file:///x"},
					}}},
				}, nil
			case "textDocument/documentSymbol":
				at := func(l1, c1, l2, c2 int) map[string]any {
					return map[string]any{
						"start": map[string]any{"line": l1, "character": c1},
						"end":   map[string]any{"line": l2, "character": c2},
					}
				}
				return []map[string]any{{
					"name": "Outer", "kind": 23,
					"range": at(0, 0, 2, 1), "selectionRange": at(0, 5, 0, 10),
					"children": []map[string]any{{
						"name": "inner", "kind": 8,
						"range": at(1, 1, 1, 10), "selectionRange": at(1, 1, 1, 6),
					}},
				}}, nil
			case "workspace/symbol":
				var p struct {
					TextDocument struct {
						URI string `json:"uri"`
					} `json:"textDocument"`
				}
				_ = json.Unmarshal(params, &p)
				at := func(l, c1, c2 int) map[string]any {
					return map[string]any{
						"start": map[string]any{"line": l, "character": c1},
						"end":   map[string]any{"line": l, "character": c2},
					}
				}
				// The fake finds the other file next to the one named in
				// FAKE_SYMBOL_DIR (the test sets it); there is no document here.
				otherURI := os.Getenv("FAKE_SYMBOL_OTHER_URI")
				return []map[string]any{
					{"name": "Foo", "kind": 12, "containerName": "pkg",
						"location": map[string]any{"uri": otherURI, "range": at(1, 2, 5)}},
					{"name": "NoRange", "kind": 13,
						"location": map[string]any{"uri": otherURI}},
				}, nil
			case "textDocument/formatting":
				return []map[string]any{{
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 0},
						"end":   map[string]any{"line": 0, "character": 4},
					},
					"newText": "FMT",
				}}, nil
			case "textDocument/rename":
				var p struct {
					TextDocument struct {
						URI string `json:"uri"`
					} `json:"textDocument"`
					NewName string `json:"newName"`
				}
				_ = json.Unmarshal(params, &p)
				newText := p.NewName
				if sawZZ.Load() {
					newText += "+ZZ" // proves that the other documents' edits arrived first
				}
				rng := func(l, c1, c2 int) map[string]any {
					return map[string]any{
						"start": map[string]any{"line": l, "character": c1},
						"end":   map[string]any{"line": l, "character": c2},
					}
				}
				other := strings.TrimSuffix(p.TextDocument.URI, "a.go") + "other.go"
				return map[string]any{"changes": map[string]any{
					p.TextDocument.URI: []map[string]any{{"range": rng(1, 4, 5), "newText": newText}},
					other:              []map[string]any{{"range": rng(1, 4, 7), "newText": newText}},
				}}, nil
			case "textDocument/references":
				var p struct {
					TextDocument struct {
						URI string `json:"uri"`
					} `json:"textDocument"`
				}
				_ = json.Unmarshal(params, &p)
				at := func(l, c1, c2 int) map[string]any {
					return map[string]any{
						"start": map[string]any{"line": l, "character": c1},
						"end":   map[string]any{"line": l, "character": c2},
					}
				}
				return []map[string]any{
					{"uri": p.TextDocument.URI, "range": at(1, 5, 6)},
					{"uri": p.TextDocument.URI, "range": at(0, 0, 7)},
				}, nil
			case "textDocument/definition":
				var p struct {
					TextDocument struct {
						URI string `json:"uri"`
					} `json:"textDocument"`
				}
				_ = json.Unmarshal(params, &p)
				rng := func(l, c1, c2 int) map[string]any {
					return map[string]any{
						"start": map[string]any{"line": l, "character": c1},
						"end":   map[string]any{"line": l, "character": c2},
					}
				}
				return []map[string]any{
					{"uri": p.TextDocument.URI, "range": rng(0, 1, 3)}, // in the document itself
					{"uri": "file:///nonexistent/other.go", "range": rng(1, 2, 4)},
				}, nil
			case "initialize":
				return map[string]any{
					"capabilities": map[string]any{
						"positionEncoding": "utf-8",
						"textDocumentSync": map[string]any{
							"openClose": true,
							"change":    2,
							"save":      map[string]any{"includeText": false},
						},
						"hoverProvider":      true,
						"definitionProvider": true,
						"referencesProvider": true,
						"renameProvider":     true,
						"documentSymbolProvider":   true,
						"codeActionProvider":       true,
						"workspaceSymbolProvider":  true,
						"documentFormattingProvider": true,
						"completionProvider": map[string]any{},
					},
					"serverInfo": map[string]any{"name": "fake"},
				}, nil
			case "shutdown":
				if mode == "hang" {
					select {} // never answer
				}
				shutdownSeen.Store(true)
				return nil, nil
			case "boom":
				os.Exit(3)
			}
			return nil, &ResponseError{Code: CodeMethodNotFound, Message: method}
		},
		Notification: func(method string, params json.RawMessage) {
			// Let tests observe synchronization traffic through Logf.
			if strings.HasPrefix(method, "textDocument/") {
				fmt.Fprintf(os.Stderr, "NOTIF %s %s\n", method, params)
			}
			if method == "textDocument/didChange" && strings.Contains(string(params), `"text":"ZZ"`) {
				sawZZ.Store(true)
			}
			if method == "textDocument/didOpen" || method == "textDocument/didChange" {
				publishFakeDiagnostics(conn, params)
			}
			if method == "exit" && mode != "hang" {
				if shutdownSeen.Load() {
					os.Exit(0)
				}
				os.Exit(1)
			}
		},
	})
	conn.Start()

	fmt.Fprintln(os.Stderr, "fake server started")
	<-conn.Done()
}

// publishFakeDiagnostics answers didOpen / didChange like a server whose
// analysis finished at once: one warning at bytes 0..3 of line 0, whose
// message names the document version it was computed for.
func publishFakeDiagnostics(conn *Conn, params json.RawMessage) {
	var p struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Version int32  `json:"version"`
		} `json:"textDocument"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	_ = conn.Notify("textDocument/publishDiagnostics", map[string]any{
		"uri":     p.TextDocument.URI,
		"version": p.TextDocument.Version,
		"diagnostics": []map[string]any{{
			"range": map[string]any{
				"start": map[string]any{"line": 0, "character": 0},
				"end":   map[string]any{"line": 0, "character": 3},
			},
			"severity": 2,
			"code":     "W1",
			"source":   "fake",
			"message":  fmt.Sprintf("fake diag v%d", p.TextDocument.Version),
		}},
	})
}

func startFake(t *testing.T, mode string) (*Client, *lockedLog) {
	t.Helper()

	log := &lockedLog{}
	c, err := StartClient(Options{
		Name:            "fake",
		Server:          ServerConfig{Command: os.Args[0], Env: []string{fakeServerEnv + "=" + mode}},
		Logf:            log.logf,
		ShutdownTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	})
	return c, log
}

type lockedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lockedLog) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestClientLifecycle(t *testing.T) {
	c, log := startFake(t, "ok")

	if c.Ready() {
		t.Fatal("Ready before Initialize")
	}
	if err := c.Initialize(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if !c.Ready() {
		t.Fatal("not Ready after Initialize")
	}
	if got := c.ServerName(); got != "fake" {
		t.Fatalf("ServerName = %q", got)
	}

	caps := c.Capabilities()
	if caps.Encoding() != PositionEncodingUTF8 {
		t.Fatalf("Encoding = %q", caps.Encoding())
	}
	if s := caps.Sync(); !s.OpenClose || s.Change != SyncIncremental || !s.Save || s.SaveIncludeText {
		t.Fatalf("Sync = %+v", s)
	}
	if !caps.HasHover() || caps.HasDefinition() {
		t.Fatal("provider flags wrong")
	}

	if err := c.Initialize(ctx5(t)); err == nil {
		t.Fatal("second Initialize must fail")
	}

	if err := c.Shutdown(ctx5(t)); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Shutdown returned before the process exited")
	}
	if c.Ready() {
		t.Fatal("Ready after Shutdown")
	}

	// Shutdown is idempotent.
	if err := c.Shutdown(ctx5(t)); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}

	if !strings.Contains(log.String(), "fake server started") {
		t.Fatalf("stderr was not forwarded: %q", log.String())
	}
}

func TestClientShutdownKillsHangingServer(t *testing.T) {
	c, _ := startFake(t, "hang")
	if err := c.Initialize(ctx5(t)); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err := c.Shutdown(ctx5(t))
	if err == nil {
		t.Fatal("want an error for a server that had to be killed")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("Shutdown took %v", time.Since(start))
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("process still running after Shutdown")
	}
}

func TestClientShutdownBeforeInitialize(t *testing.T) {
	c, _ := startFake(t, "ok")
	// Never initialized: the process is simply terminated.
	if err := c.Shutdown(ctx5(t)); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("process still running")
	}
}

func TestClientServerCrash(t *testing.T) {
	c, _ := startFake(t, "crash")
	if err := c.Initialize(ctx5(t)); err != nil {
		t.Fatal(err)
	}

	err := c.Conn().Call(ctx5(t), "boom", nil, nil)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want ErrClosed", err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after the server died")
	}

	// Shutdown after a crash must still succeed in cleaning up.
	_ = c.Shutdown(ctx5(t))
}

func TestStartClientErrors(t *testing.T) {
	if _, err := StartClient(Options{}); err == nil {
		t.Fatal("empty command must fail")
	}
	if _, err := StartClient(Options{Server: ServerConfig{Command: "/nonexistent/ge-lsp-test"}}); err == nil {
		t.Fatal("missing binary must fail")
	}
}

func TestSyncOptionsForms(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want SyncOptions
	}{
		{"absent", ``, SyncOptions{}},
		{"null", `null`, SyncOptions{}},
		{"number full", `1`, SyncOptions{OpenClose: true, Change: SyncFull}},
		{"number none", `0`, SyncOptions{OpenClose: true, Change: SyncNone}},
		{"object save true", `{"openClose":true,"change":2,"save":true}`,
			SyncOptions{OpenClose: true, Change: SyncIncremental, Save: true}},
		{"object save text", `{"openClose":true,"change":1,"save":{"includeText":true}}`,
			SyncOptions{OpenClose: true, Change: SyncFull, Save: true, SaveIncludeText: true}},
		{"object save false", `{"openClose":true,"change":2,"save":false}`,
			SyncOptions{OpenClose: true, Change: SyncIncremental}},
		{"object no save", `{"openClose":false,"change":1}`, SyncOptions{Change: SyncFull}},
	}
	for _, tt := range tests {
		got := ServerCapabilities{TextDocumentSync: json.RawMessage(tt.raw)}.Sync()
		if got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestProviderEnabled(t *testing.T) {
	for raw, want := range map[string]bool{
		``: false, `null`: false, `false`: false,
		`true`: true, `{"workDoneProgress":true}`: true,
	} {
		if got := providerEnabled(json.RawMessage(raw)); got != want {
			t.Errorf("providerEnabled(%q) = %v", raw, got)
		}
	}
}

func TestEncodingDefault(t *testing.T) {
	if got := (ServerCapabilities{}).Encoding(); got != PositionEncodingUTF16 {
		t.Fatalf("default encoding = %q, want utf-16", got)
	}
}

func TestFileURI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path expectations are Unix-style")
	}

	uri := FileURI("/tmp/a b/あ.go")
	if want := "file:///tmp/a%20b/%E3%81%82.go"; uri != want {
		t.Fatalf("FileURI = %q, want %q", uri, want)
	}
	back, err := URIToPath(uri)
	if err != nil || back != "/tmp/a b/あ.go" {
		t.Fatalf("URIToPath = %q, %v", back, err)
	}

	if _, err := URIToPath("https://example.com/x"); err == nil {
		t.Fatal("non-file URI must fail")
	}
}
