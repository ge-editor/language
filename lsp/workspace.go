package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager is the LSP side of one Workspace: it owns the language server
// processes and keeps every open file in sync with its server.
//
//	Workspace
//	  └─ Manager                     one per workspace root
//	       ├─ server (gopls)         one process per distinct command
//	       │    └─ Client / Conn
//	       └─ session per buffer     one goroutine per open document
//	            └─ Document          mirror + didOpen/didChange/...
//
// Leaves never touch any of this. The application calls Open when a buffer
// appears, Close when it is killed and Shutdown at quit; saves need no
// call, because the buffer announces them (Buffer.AddSaveListener).
//
// Threading. Open, Close and Shutdown (the "UI methods") must be
// called on the UI goroutine, the same goroutine that mutates the
// EditBuffer. The Manager's maps are touched only there, so they need no
// lock. Everything slow (starting and initializing a server, sending) runs
// on other goroutines, and the only thing that crosses back to the UI
// goroutine is a closure handed to Options.Dispatch.

var (
	// ErrNoServer: no language server is configured for the language.
	ErrNoServer = errors.New("lsp: no language server configured for this language")
	// ErrNoPath: the buffer has no file path, so it has no document URI.
	ErrNoPath = errors.New("lsp: buffer has no file path")
	// ErrManagerClosed: Shutdown has been called.
	ErrManagerClosed = errors.New("lsp: manager is shut down")
)

// Buffer is what the Manager needs from an editor buffer. All methods are
// called on the UI goroutine. The ebadapter subpackage implements it for
// editbuffer.EditBuffer; keeping it an interface keeps this package free
// of any editor dependency.
type Buffer interface {
	// Key identifies the buffer (a comparable value, normally the pointer
	// to the underlying buffer). Open and Close match on it.
	Key() any

	// Path is the absolute file path, or "" if the buffer has none.
	Path() string

	// Snapshot returns an independent copy of all rows and the buffer
	// version of exactly that content.
	Snapshot() (rows [][]byte, version uint64)

	// Version returns the current buffer version without copying anything.
	Version() uint64

	// AddChangeListener registers fn for every later modification, in
	// order, synchronously on the UI goroutine, and returns the function
	// that unregisters it. fn must not block. Change.Text must not alias
	// the live buffer.
	AddChangeListener(fn func(Change)) (remove func())

	// AddSaveListener registers fn to be called after each successful
	// save, on the UI goroutine, and returns the function that unregisters
	// it. A save-time reformat must already have been reported through the
	// change listener by then, so that didSave follows the final text.
	AddSaveListener(fn func()) (remove func())
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	// RootPath is the workspace root passed to every server.
	RootPath string

	// Servers returns how to start the server for a language ID, or false
	// if there is none. It comes from the language package's
	// configuration; the Manager does the starting.
	Servers func(languageID string) (ServerConfig, bool)

	// Dispatch runs f on the UI goroutine, later. It must not block and may
	// be called from any goroutine.
	Dispatch func(f func())

	// Handler receives server-initiated messages. It is shared by all
	// servers of this Manager and is called after the Manager has handled
	// the messages it understands itself (publishDiagnostics).
	Handler Handler

	// OnDiagnostics receives the diagnostics of an open buffer, converted
	// to ge coordinates, on the UI goroutine. Each call replaces the
	// buffer's previous set; an empty set clears it. The positions refer
	// to the buffer version in DiagnosticsUpdate.Version: if the buffer is
	// at another version the update is stale and must be ignored.
	OnDiagnostics func(buf Buffer, u DiagnosticsUpdate)

	// Logf receives server stderr and the Manager's own messages.
	Logf func(format string, args ...any)

	// InitTimeout bounds the initialize request. Default 30s.
	InitTimeout time.Duration

	// ShutdownTimeout is passed to each Client. Default 3s.
	ShutdownTimeout time.Duration
}

// Manager: see the package comment above.
type Manager struct {
	opts ManagerOptions

	// UI goroutine only.
	servers  map[string]*server
	sessions map[any]*session
	closed   bool

	// byURI finds the session of a document for messages that name it by
	// URI. Unlike the maps above it is read on the servers' read goroutines.
	uriMu sync.RWMutex
	byURI map[string]*session
}

// NewManager returns a Manager. Servers and Dispatch are required.
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Servers == nil {
		return nil, errors.New("lsp: ManagerOptions.Servers is required")
	}
	if opts.Dispatch == nil {
		return nil, errors.New("lsp: ManagerOptions.Dispatch is required")
	}
	if opts.InitTimeout <= 0 {
		opts.InitTimeout = 30 * time.Second
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 3 * time.Second
	}
	return &Manager{
		opts:     opts,
		servers:  make(map[string]*server),
		sessions: make(map[any]*session),
		byURI:    make(map[string]*session),
	}, nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.opts.Logf != nil {
		m.opts.Logf(format, args...)
	}
}

// server is one language server process of this workspace.
type server struct {
	key    string
	client *Client

	ready chan struct{} // closed when Initialize finished (successfully or not)
	err   error         // initialize failure; valid after ready is closed
}

func serverKey(cfg ServerConfig) string {
	return cfg.Command + "\x00" + strings.Join(cfg.Args, "\x00")
}

// server returns the running server for cfg, starting one if needed. A
// server that crashed after a successful start is replaced; one whose
// initialize failed stays as a tombstone, so a broken setup is reported
// once per Manager instead of being respawned for every file.
func (m *Manager) server(cfg ServerConfig) (*server, error) {
	key := serverKey(cfg)

	if srv, ok := m.servers[key]; ok {
		select {
		case <-srv.ready:
			if srv.err != nil {
				return nil, fmt.Errorf("lsp: %s failed to initialize: %w", cfg.Command, srv.err)
			}
			select {
			case <-srv.client.Done():
				m.logf("lsp: %s exited unexpectedly, restarting", cfg.Command)
				delete(m.servers, key)
			default:
				return srv, nil
			}
		default:
			return srv, nil // still initializing
		}
	}

	client, err := StartClient(Options{
		Name:            filepath.Base(cfg.Command),
		Server:          cfg,
		RootPath:        m.opts.RootPath,
		Handler:         Handler{Request: m.opts.Handler.Request, Notification: m.notify},
		Logf:            m.opts.Logf,
		ShutdownTimeout: m.opts.ShutdownTimeout,
	})
	if err != nil {
		return nil, err
	}

	srv := &server{key: key, client: client, ready: make(chan struct{})}
	m.servers[key] = srv

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.opts.InitTimeout)
		defer cancel()

		err := client.Initialize(ctx)
		srv.err = err
		close(srv.ready)

		if err != nil {
			m.logf("lsp: %s: %v", cfg.Command, err)
			sctx, scancel := context.WithTimeout(context.Background(), 2*m.opts.ShutdownTimeout)
			defer scancel()
			_ = client.Shutdown(sctx)
		}
	}()
	return srv, nil
}

// notify handles server notifications on the server's read goroutine: it
// routes publishDiagnostics to the session of the document, then passes
// every message on to the caller's handler.
func (m *Manager) notify(method string, params json.RawMessage) {
	if method == "textDocument/publishDiagnostics" {
		p, err := parsePublishDiagnostics(params)
		if err != nil {
			m.logf("%v", err)
		} else {
			m.uriMu.RLock()
			s := m.byURI[p.URI]
			m.uriMu.RUnlock()
			if s != nil {
				s.q.push(event{diag: p})
			}
		}
	}

	if h := m.opts.Handler.Notification; h != nil {
		h(method, params)
	}
}

// Open starts syncing buf with the server for languageID. It returns
// quickly: the server is started and initialized in the background, and
// the document is opened as soon as it is ready. Changes made meanwhile are
// queued, not lost.
//
// Calling Open for a buffer that is already open is a no-op.
func (m *Manager) Open(buf Buffer, languageID string) error {
	if m.closed {
		return ErrManagerClosed
	}

	key := buf.Key()
	if s, ok := m.sessions[key]; ok {
		select {
		case <-s.done: // its server died; reopen below
			s.close()
			delete(m.sessions, key)
		default:
			return nil
		}
	}

	path := buf.Path()
	if path == "" {
		return ErrNoPath
	}
	cfg, ok := m.opts.Servers(languageID)
	if !ok {
		return ErrNoServer
	}
	srv, err := m.server(cfg)
	if err != nil {
		return err
	}

	// Text, version and listener registration happen in this one UI
	// goroutine step, so no change can fall between the snapshot and the
	// first queued event.
	rows, version := buf.Snapshot()
	s := &session{
		m:           m,
		buf:         buf,
		srv:         srv,
		uri:         FileURI(path),
		languageID:  languageID,
		openRows:    rows,
		openVersion: version,
		q:           newEventQueue(),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		resync:      make(chan snapshot, 1),
	}
	removeChange := buf.AddChangeListener(func(c Change) { s.q.push(event{change: c}) })
	removeSave := buf.AddSaveListener(func() { s.q.push(event{save: true}) })
	s.remove = func() {
		removeChange()
		removeSave()
	}
	m.sessions[key] = s
	m.uriMu.Lock()
	m.byURI[s.uri] = s
	m.uriMu.Unlock()

	go s.run()
	return nil
}

// Close stops syncing buf: didClose is sent (if the server is still alive)
// and the change listener is removed. The server keeps running for other
// documents. Closing a buffer that is not open is a no-op.
func (m *Manager) Close(buf Buffer) {
	key := buf.Key()
	s, ok := m.sessions[key]
	if !ok {
		return
	}
	delete(m.sessions, key)
	s.close()
}

// Shutdown closes every document, then shuts every server down
// gracefully (shutdown request, exit notification, kill after
// ShutdownTimeout). It returns when all processes are gone or ctx ends.
// The Manager cannot be used afterwards. Calling it again is a no-op.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m.closed {
		return nil
	}
	m.closed = true

	sessions := m.sessions
	m.sessions = make(map[any]*session)
	for _, s := range sessions {
		s.close()
	}

	var errs []error

	// Let the sessions send didClose before the servers are told to exit.
wait:
	for _, s := range sessions {
		select {
		case <-s.done:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("lsp: closing documents: %w", ctx.Err()))
			break wait
		}
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, srv := range m.servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.client.Shutdown(ctx); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	m.servers = make(map[string]*server)

	return errors.Join(errs...)
}

// ---- session: one open document ----

type snapshot struct {
	rows    [][]byte
	version uint64
}

type session struct {
	m          *Manager
	buf        Buffer
	srv        *server
	uri        string
	languageID string

	openRows    [][]byte
	openVersion uint64

	q      *eventQueue
	remove func()        // unregisters the change and save listeners
	stop   chan struct{} // closed by close
	done   chan struct{} // closed when run returned
	resync chan snapshot // fresh buffer snapshot after ErrDesynced

	closed bool // UI goroutine only

	// The latest diagnostics the server published for the document, and the
	// version they are for. Session goroutine only.
	diags        []wireDiagnostic
	diagsVersion uint64
}

// close detaches the session from the buffer and asks run to finish.
// UI goroutine only; idempotent.
func (s *session) close() {
	if s.closed {
		return
	}
	s.closed = true
	s.remove()

	s.m.uriMu.Lock()
	if s.m.byURI[s.uri] == s {
		delete(s.m.byURI, s.uri)
	}
	s.m.uriMu.Unlock()

	close(s.stop)
}

// run is the session goroutine. It owns the Document and is the only
// code that sends this document's notifications, so they leave in order.
func (s *session) run() {
	defer close(s.done)
	// However run ends (closed, server died), stop feeding the queue:
	// otherwise the listener would grow it forever.
	defer s.m.opts.Dispatch(s.close)

	srv := s.srv
	select {
	case <-srv.ready:
	case <-s.stop:
		return
	}
	if srv.err != nil {
		return // already logged by whoever started the server
	}
	client := srv.client

	doc := NewDocument(s.uri, s.languageID, client.Capabilities(), s.openVersion, s.openRows)
	s.openRows = nil
	if err := client.DidOpen(doc); err != nil {
		s.m.logf("lsp: didOpen %s: %v", s.uri, err)
		return
	}
	defer func() { _ = client.DidClose(doc) }() // best effort; fails quietly if the server is gone

	resyncing := false

	for {
		select {
		case <-s.stop:
			return
		case <-client.Done():
			return

		case snap := <-s.resync:
			if err := client.DidChange(doc.Resync(snap.version, snap.rows)); err != nil {
				s.m.logf("lsp: resync %s: %v", s.uri, err)
			}
			resyncing = false

		case <-s.q.wake:
		}

		var batch *DidChangeParams
		flush := func() {
			if batch == nil {
				return
			}
			if err := client.DidChange(batch); err != nil {
				s.m.logf("lsp: didChange %s: %v", s.uri, err)
			}
			batch = nil
		}

		for _, e := range s.q.drain() {
			if e.diag != nil {
				s.publishDiagnostics(doc, e.diag, resyncing)
				continue
			}
			if e.run != nil {
				flush() // the server must have seen the edits before the question
				e.run(doc, client)
				continue
			}
			if e.save {
				flush()
				if !resyncing { // the server's text is unreliable until resynced
					if err := client.DidSave(doc); err != nil {
						s.m.logf("lsp: didSave %s: %v", s.uri, err)
					}
				}
				continue
			}

			p, err := doc.Apply(e.change)
			if err != nil {
				flush() // the server gets everything up to the bad change
				if !resyncing {
					resyncing = true
					s.m.logf("lsp: %s: %v; resyncing", s.uri, err)
					s.requestResync()
				}
				continue
			}
			if p == nil {
				continue
			}
			// Consecutive changes travel in one didChange, in order.
			if batch == nil {
				batch = p
			} else {
				batch.ContentChanges = append(batch.ContentChanges, p.ContentChanges...)
				batch.TextDocument = p.TextDocument
			}
		}
		flush()
	}
}

// publishDiagnostics converts a server's diagnostics with the mirror and
// hands them to the UI goroutine.
//
// Only a result for exactly the mirror's version is converted: for older
// text the ranges would be mapped through the wrong lines, and the server
// publishes again once it has processed the newer text.
func (s *session) publishDiagnostics(doc *Document, p *publishDiagnosticsParams, resyncing bool) {
	if resyncing {
		return
	}
	if p.Version != nil && *p.Version != int32(doc.Version()) {
		return
	}

	// Kept for code actions, which must be asked about the diagnostics that
	// are at the range (the servers offer quick fixes for them).
	s.diags, s.diagsVersion = p.Diagnostics, doc.Version()

	if s.m.opts.OnDiagnostics == nil {
		return
	}
	u := DiagnosticsUpdate{Version: doc.Version(), Items: doc.convertDiagnostics(p.Diagnostics)}
	s.m.opts.Dispatch(func() {
		if !s.closed { // UI goroutine
			s.m.opts.OnDiagnostics(s.buf, u)
		}
	})
}

// requestResync asks the UI goroutine for a fresh snapshot of the buffer.
func (s *session) requestResync() {
	s.m.opts.Dispatch(func() {
		if s.closed {
			return
		}
		rows, version := s.buf.Snapshot()
		select {
		case s.resync <- snapshot{rows: rows, version: version}:
		default: // one is already on its way
		}
	})
}

// ---- event queue ----

// event is one item for the session goroutine: a buffer change or a
// "saved" mark (from the save listener). Keeping both in one queue preserves their relative order.
type event struct {
	change Change
	save   bool
	diag   *publishDiagnosticsParams // from the server, for this document

	// run executes on the session goroutine with the document's mirror,
	// after every change queued before it has been sent. It carries
	// requests (hover, definition) that need the mirror and the order.
	run func(doc *Document, client *Client)
}

// eventQueue is an unbounded queue with a wake-up channel. push never
// blocks, which the change listener (UI goroutine) requires.
type eventQueue struct {
	mu    sync.Mutex
	items []event
	wake  chan struct{} // capacity 1
}

func newEventQueue() *eventQueue {
	return &eventQueue{wake: make(chan struct{}, 1)}
}

func (q *eventQueue) push(e event) {
	q.mu.Lock()
	q.items = append(q.items, e)
	q.mu.Unlock()

	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *eventQueue) drain() []event {
	q.mu.Lock()
	items := q.items
	q.items = nil
	q.mu.Unlock()
	return items
}
