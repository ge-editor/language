package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fake buffer ----

// fakeBuffer behaves like an EditBuffer as far as the Manager can tell:
// edits bump the version and call the registered listeners synchronously
// on the calling goroutine, which plays the UI goroutine.
type fakeBuffer struct {
	mu        sync.Mutex
	path      string
	rows      [][]byte
	version   uint64
	listeners map[int]func(Change)
	next      int

	saveListeners map[int]func()
}

func newFakeBuffer(path string, lines ...string) *fakeBuffer {
	return &fakeBuffer{
		path:      path,
		rows:      rows(lines...),
		listeners: make(map[int]func(Change)),

		saveListeners: make(map[int]func()),
	}
}

func (b *fakeBuffer) Key() any { return b }

func (b *fakeBuffer) Path() string { return b.path }

func (b *fakeBuffer) Version() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
}

func (b *fakeBuffer) Snapshot() ([][]byte, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneLines(b.rows), b.version
}

func (b *fakeBuffer) AddChangeListener(fn func(Change)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	b.listeners[id] = fn
	return func() {
		b.mu.Lock()
		delete(b.listeners, id)
		b.mu.Unlock()
	}
}

func (b *fakeBuffer) AddSaveListener(fn func()) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	b.saveListeners[id] = fn
	return func() {
		b.mu.Lock()
		delete(b.saveListeners, id)
		b.mu.Unlock()
	}
}

func (b *fakeBuffer) listenerCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.listeners) + len(b.saveListeners)
}

// save simulates a successful Save: the save listeners run.
func (b *fakeBuffer) save() {
	b.mu.Lock()
	fns := make([]func(), 0, len(b.saveListeners))
	for _, fn := range b.saveListeners {
		fns = append(fns, fn)
	}
	b.mu.Unlock()

	for _, fn := range fns {
		fn()
	}
}

// insert edits one line like InsertRegion would and notifies.
func (b *fakeBuffer) insert(row, col int, text string) {
	b.mu.Lock()
	line := b.rows[row]
	b.rows[row] = concat(line[:col], []byte(text), line[col:])
	b.version++
	c := Change{
		Version: b.version,
		Kind:    ChangeInsert,
		Start:   Pos{Row: row, Col: col},
		Text:    rows(text),
	}
	b.mu.Unlock()

	b.emit(c)
}

// emit delivers c to the listeners as is, without touching the buffer.
func (b *fakeBuffer) emit(c Change) {
	b.mu.Lock()
	fns := make([]func(Change), 0, len(b.listeners))
	for _, fn := range b.listeners {
		fns = append(fns, fn)
	}
	b.mu.Unlock()

	for _, fn := range fns {
		fn(c)
	}
}

// setState replaces the content silently, as if changes had been missed.
func (b *fakeBuffer) setState(version uint64, lines ...string) {
	b.mu.Lock()
	b.rows = rows(lines...)
	b.version = version
	b.mu.Unlock()
}

// ---- harness ----

type harness struct {
	t    *testing.T
	m    *Manager
	log  *lockedLog
	root string
	ui   chan func() // closures the Manager dispatched to "the UI goroutine"

	diagMu sync.Mutex
	diags  []DiagnosticsUpdate
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{
		t:    t,
		log:  &lockedLog{},
		root: t.TempDir(),
		ui:   make(chan func(), 256),
	}

	m, err := NewManager(ManagerOptions{
		RootPath: h.root,
		Servers: func(languageID string) (ServerConfig, bool) {
			if languageID != "go" {
				return ServerConfig{}, false
			}
			return ServerConfig{
				Command: os.Args[0],
				Env:     []string{fakeServerEnv + "=ok"},
			}, true
		},
		Dispatch: func(f func()) {
			select {
			case h.ui <- f:
			default: // the test is over
			}
		},
		OnDiagnostics: func(_ Buffer, u DiagnosticsUpdate) {
			h.diagMu.Lock()
			h.diags = append(h.diags, u)
			h.diagMu.Unlock()
		},
		Logf:            h.log.logf,
		ShutdownTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.m = m

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return h
}

// pump runs, on the calling (test) goroutine, what the Manager dispatched
// to the UI goroutine.
func (h *harness) pump() {
	for {
		select {
		case f := <-h.ui:
			f()
		default:
			return
		}
	}
}

// waitLog waits until the server's log contains substr, serving the UI
// queue meanwhile.
func (h *harness) waitLog(substr string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.pump()
		if strings.Contains(h.log.String(), substr) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %q in log:\n%s", substr, h.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitDiag waits for a diagnostics update with the given buffer version and
// returns it.
func (h *harness) waitDiag(version uint64) DiagnosticsUpdate {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.pump()
		h.diagMu.Lock()
		for _, u := range h.diags {
			if u.Version == version {
				h.diagMu.Unlock()
				return u
			}
		}
		h.diagMu.Unlock()
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for diagnostics of version %d; log:\n%s", version, h.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) path(name string) string { return filepath.Join(h.root, name) }

// ---- tests ----

func TestManagerSyncLifecycle(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "package a")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	log := h.log.String()
	for _, want := range []string{
		`"languageId":"go"`,
		`"version":0`,
		`"text":"package a"`,
		FileURI(h.path("a.go")),
	} {
		if !strings.Contains(log, want) {
			t.Errorf("didOpen lacks %s:\n%s", want, log)
		}
	}

	buf.insert(0, 9, " // x")
	h.waitLog(`"text":" // x"`)
	h.waitLog("NOTIF textDocument/didChange")

	buf.save()
	h.waitLog("NOTIF textDocument/didSave")

	h.m.Close(buf)
	h.waitLog("NOTIF textDocument/didClose")

	if buf.listenerCount() != 0 {
		t.Fatal("change listener still registered after Close")
	}

	// The four notifications arrived in this order.
	log = h.log.String()
	last := -1
	for _, m := range []string{"didOpen", "didChange", "didSave", "didClose"} {
		i := strings.Index(log, "NOTIF textDocument/"+m)
		if i < 0 || i < last {
			t.Fatalf("%s out of order or missing in:\n%s", m, log)
		}
		last = i
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestManagerKeepsEditOrder(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}

	// Edits made while the server is still starting are queued, then
	// sent in order. Whether they travel in one didChange or several is
	// timing, not contract.
	buf.insert(0, 0, "ZA")
	buf.insert(0, 2, "ZB")
	buf.insert(0, 4, "ZC")

	h.waitLog(`"text":"ZC"`)

	log := h.log.String()
	a, b, c := strings.Index(log, `"text":"ZA"`), strings.Index(log, `"text":"ZB"`), strings.Index(log, `"text":"ZC"`)
	if a < 0 || b < 0 || a > b || b > c {
		t.Fatalf("edits missing or out of order (ZA=%d ZB=%d ZC=%d):\n%s", a, b, c, log)
	}
}

func TestManagerTwoBuffersShareOneServer(t *testing.T) {
	h := newHarness(t)
	a := newFakeBuffer(h.path("a.go"), "package a")
	b := newFakeBuffer(h.path("b.go"), "package b")

	if err := h.m.Open(a, "go"); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Open(b, "go"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.m.servers); n != 1 {
		t.Fatalf("%d servers started, want 1", n)
	}

	h.waitLog(`"text":"package a"`)
	h.waitLog(`"text":"package b"`)

	// Opening again is a no-op.
	if err := h.m.Open(a, "go"); err != nil {
		t.Fatal(err)
	}
	if a.listenerCount() != 2 { // one change listener and one save listener
		t.Fatalf("listener count = %d after a repeated Open", a.listenerCount())
	}
}

func TestManagerResyncAfterMissedChange(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "one")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}
	h.waitLog("NOTIF textDocument/didOpen")

	// The buffer moved on to version 9 with different text and the
	// Manager saw only a change with version 3: a gap.
	buf.setState(9, "fresh")
	buf.emit(Change{Version: 3, Kind: ChangeInsert, Start: Pos{0, 0}, Text: rows("x")})

	h.waitLog(`"text":"fresh"`) // full-text resync from a new snapshot
	h.waitLog(`"version":9`)

	// Syncing continues incrementally from the resynced version.
	buf.insert(0, 5, "ZQ")
	h.waitLog(`"text":"ZQ"`)
}

func TestManagerErrors(t *testing.T) {
	h := newHarness(t)

	if err := h.m.Open(newFakeBuffer("", "x"), "go"); !errors.Is(err, ErrNoPath) {
		t.Errorf("no path: got %v", err)
	}
	if err := h.m.Open(newFakeBuffer(h.path("a.rs"), "x"), "rust"); !errors.Is(err, ErrNoServer) {
		t.Errorf("no server: got %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Open(newFakeBuffer(h.path("a.go"), "x"), "go"); !errors.Is(err, ErrManagerClosed) {
		t.Errorf("after Shutdown: got %v", err)
	}
	if err := h.m.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}

func TestManagerMissingServerBinary(t *testing.T) {
	m, err := NewManager(ManagerOptions{
		Servers: func(string) (ServerConfig, bool) {
			return ServerConfig{Command: "/nonexistent/ge-lsp-test"}, true
		},
		Dispatch: func(f func()) {},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = m.Open(newFakeBuffer("/tmp/x.go", "x"), "go")
	if err == nil || errors.Is(err, ErrNoServer) {
		t.Fatalf("got %v, want the start error", err)
	}
}

func TestNewManagerValidatesOptions(t *testing.T) {
	if _, err := NewManager(ManagerOptions{Dispatch: func(func()) {}}); err == nil {
		t.Error("missing Servers must fail")
	}
	if _, err := NewManager(ManagerOptions{Servers: func(string) (ServerConfig, bool) { return ServerConfig{}, false }}); err == nil {
		t.Error("missing Dispatch must fail")
	}
}

func TestManagerDeliversDiagnostics(t *testing.T) {
	h := newHarness(t)
	buf := newFakeBuffer(h.path("a.go"), "package a")

	if err := h.m.Open(buf, "go"); err != nil {
		t.Fatal(err)
	}

	u := h.waitDiag(0) // the fake server answers didOpen
	if len(u.Items) != 1 {
		t.Fatalf("items = %+v", u.Items)
	}
	d := u.Items[0]
	if d.Start != (Pos{0, 0}) || d.End != (Pos{0, 3}) || d.Severity != SeverityWarning ||
		d.Code != "W1" || d.Source != "fake" || d.Message != "fake diag v0" {
		t.Fatalf("diagnostic = %+v", d)
	}

	// An edit makes the server analyze again and publish for the new version.
	buf.insert(0, 0, "Z")
	u = h.waitDiag(1)
	if len(u.Items) != 1 || u.Items[0].Message != "fake diag v1" {
		t.Fatalf("after edit: %+v", u.Items)
	}

	// After Close, the URI is forgotten: nothing more is routed.
	h.m.Close(buf)
	h.m.uriMu.RLock()
	n := len(h.m.byURI)
	h.m.uriMu.RUnlock()
	if n != 0 {
		t.Fatalf("%d URIs still registered after Close", n)
	}
}

