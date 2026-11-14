package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// ErrClosed is returned (possibly wrapped) by calls on a Conn whose
// connection ended: the server exited, the pipe closed, or the stream
// was corrupt.
var ErrClosed = errors.New("lsp: connection closed")

// Handler receives what the server initiates. Both fields are optional.
type Handler struct {
	// Request handles a request from the server (workspace/configuration,
	// client/registerCapability, window/workDoneProgress/create, ...).
	// It runs on its own goroutine; ctx is cancelled when the server sends
	// $/cancelRequest for it or the connection ends. The returned value is
	// marshaled as the result; a *ResponseError is sent as is, any other
	// error becomes CodeInternalError. With no handler every request is
	// answered with CodeMethodNotFound, so the server is never left waiting.
	Request func(ctx context.Context, method string, params json.RawMessage) (any, error)

	// Notification handles a notification from the server
	// (textDocument/publishDiagnostics, window/logMessage, ...).
	//
	// It runs on the read goroutine, in the order the server sent them,
	// which matters for diagnostics. It must return quickly and must NOT
	// call Conn.Call (the read loop would wait for itself); copy what is
	// needed and hand it to another goroutine.
	Notification func(method string, params json.RawMessage)
}

// Conn is one JSON-RPC 2.0 connection. It is safe for concurrent use.
//
// Conn does not own the underlying streams. For a server process the owner
// (the client in client.go) closes them and waits for the process; Conn
// notices through the read side hitting EOF.
type Conn struct {
	r       *bufio.Reader
	w       io.Writer
	handler Handler

	wmu sync.Mutex // serializes writes

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan *wireMessage
	inflight map[string]context.CancelFunc // server requests being handled, by raw id
	err      error                         // set once, before done is closed

	startOnce sync.Once
	done      chan struct{}
}

// NewConn returns a Conn reading from r and writing to w. Call Start to
// begin reading.
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	return &Conn{
		r:        bufio.NewReaderSize(r, 64<<10),
		w:        w,
		handler:  h,
		pending:  make(map[int64]chan *wireMessage),
		inflight: make(map[string]context.CancelFunc),
		done:     make(chan struct{}),
	}
}

// Start launches the read goroutine. It is idempotent.
func (c *Conn) Start() {
	c.startOnce.Do(func() { go c.readLoop() })
}

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended (wrapping ErrClosed), or nil while
// it is still open.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Call sends a request and waits for its response.
//
// params may be nil (the field is then omitted). If result is non-nil the
// response's result is decoded into it; a JSON null leaves it untouched.
// A server error is returned as *ResponseError.
//
// If ctx ends first, Call sends $/cancelRequest, forgets the request (a
// late response is discarded) and returns ctx.Err(). If the connection
// ends first, Call returns an error wrapping ErrClosed.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	rawParams, err := marshalParams(params)
	if err != nil {
		return err
	}

	id, ch, err := c.register()
	if err != nil {
		return err
	}
	idRaw := json.RawMessage(strconv.FormatInt(id, 10))

	req := wireMessage{JSONRPC: "2.0", ID: &idRaw, Method: method, Params: rawParams}
	if err := c.write(&req); err != nil {
		c.unregister(id)
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 && string(resp.Result) != "null" {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("lsp: decoding %s result: %w", method, err)
			}
		}
		return nil

	case <-ctx.Done():
		c.unregister(id)
		// Best effort: the server may already have answered.
		_ = c.Notify("$/cancelRequest", cancelParams{ID: idRaw})
		return ctx.Err()

	case <-c.done:
		return c.Err()
	}
}

// Notify sends a notification. It does not wait for anything.
func (c *Conn) Notify(method string, params any) error {
	rawParams, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.write(&wireMessage{JSONRPC: "2.0", Method: method, Params: rawParams})
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("lsp: encoding params: %w", err)
	}
	return b, nil
}

func (c *Conn) register() (int64, chan *wireMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.done:
		return 0, nil, c.err
	default:
	}

	c.nextID++
	ch := make(chan *wireMessage, 1) // buffered: the read loop never blocks on a gone caller
	c.pending[c.nextID] = ch
	return c.nextID, ch, nil
}

func (c *Conn) unregister(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Conn) write(m *wireMessage) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("lsp: encoding message: %w", err)
	}

	select {
	case <-c.done:
		return c.Err()
	default:
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := writeFrame(c.w, body); err != nil {
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	return nil
}

// readLoop reads until the stream ends or breaks, then shuts the Conn down.
func (c *Conn) readLoop() {
	var err error
	defer func() { c.shutdown(err) }()

	for {
		var body []byte
		body, err = readFrame(c.r)
		if err != nil {
			return
		}

		var m wireMessage
		if jerr := json.Unmarshal(body, &m); jerr != nil {
			// Cannot even tell what it was, so there is nobody to answer.
			// The framing is intact, so keep going.
			continue
		}
		c.dispatch(&m)
	}
}

func (c *Conn) shutdown(cause error) {
	if cause == nil || cause == io.EOF {
		cause = ErrClosed
	} else {
		cause = fmt.Errorf("%w: %v", ErrClosed, cause)
	}

	c.mu.Lock()
	c.err = cause
	cancels := make([]context.CancelFunc, 0, len(c.inflight))
	for _, cancel := range c.inflight {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()

	close(c.done) // wakes every Call blocked in select
	for _, cancel := range cancels {
		cancel()
	}
}

func (c *Conn) dispatch(m *wireMessage) {
	switch {
	case m.Method != "" && m.ID != nil:
		c.handleRequest(m)
	case m.Method != "":
		c.handleNotification(m)
	case m.ID != nil:
		c.handleResponse(m)
	}
}

func (c *Conn) handleResponse(m *wireMessage) {
	var id int64
	if err := json.Unmarshal(*m.ID, &id); err != nil {
		return // we only ever send integer ids
	}

	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()

	if ok {
		ch <- m
	}
	// !ok: the caller gave up (ctx cancelled); drop the late response.
}

func (c *Conn) handleNotification(m *wireMessage) {
	if m.Method == "$/cancelRequest" {
		var p cancelParams
		if json.Unmarshal(m.Params, &p) == nil {
			c.mu.Lock()
			cancel := c.inflight[string(p.ID)]
			c.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		}
		return
	}

	if c.handler.Notification != nil {
		c.handler.Notification(m.Method, m.Params)
	}
}

func (c *Conn) handleRequest(m *wireMessage) {
	id := *m.ID
	key := string(id)

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.inflight[key] = cancel
	c.mu.Unlock()

	method, params := m.Method, m.Params

	go func() {
		defer func() {
			cancel()
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()

		if c.handler.Request == nil {
			c.reply(id, nil, &ResponseError{
				Code:    CodeMethodNotFound,
				Message: "method not supported by client: " + method,
			})
			return
		}
		result, err := c.handler.Request(ctx, method, params)
		c.reply(id, result, err)
	}()
}

func (c *Conn) reply(id json.RawMessage, result any, err error) {
	resp := wireMessage{JSONRPC: "2.0", ID: &id}

	if err != nil {
		var re *ResponseError
		if !errors.As(err, &re) {
			re = &ResponseError{Code: CodeInternalError, Message: err.Error()}
		}
		resp.Error = re
	} else if b, merr := json.Marshal(result); merr != nil { // nil marshals to "null"
		resp.Error = &ResponseError{Code: CodeInternalError, Message: merr.Error()}
	} else {
		resp.Result = b
	}

	_ = c.write(&resp) // if this fails the connection is going away anyway
}
