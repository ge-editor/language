package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ServerConfig says how to start one language server. It mirrors the
// config the language package provides (Command, Args); starting and
// supervising is this package's job, not the language package's.
type ServerConfig struct {
	Command string
	Args    []string
	Env     []string // appended to os.Environ()
	Dir     string   // working directory; defaults to Options.RootPath
}

// Options configures one Client.
type Options struct {
	// Name labels log lines ("gopls").
	Name string

	Server ServerConfig

	// RootPath is the workspace root. Empty means no root (single-file
	// mode); most servers then offer much less.
	RootPath string

	// InitializationOptions is passed through to the server verbatim.
	InitializationOptions any

	// Handler receives server-initiated messages that Client does not
	// handle itself. Client answers the housekeeping requests
	// (registerCapability, workDoneProgress/create, configuration) and logs
	// window/logMessage, so a nil Handler is fine for Milestone 1.
	Handler Handler

	// Logf receives server stderr lines and window/logMessage. nil discards.
	Logf func(format string, args ...any)

	// ShutdownTimeout bounds each step of Shutdown: the shutdown request,
	// then the wait for the process to exit after "exit". After that the
	// process is killed. Default 3s.
	ShutdownTimeout time.Duration
}

type clientState int

const (
	stateStarted  clientState = iota // process running, not initialized
	stateReady                       // initialize/initialized done
	stateStopping                    // Shutdown in progress
)

// Client is one language server process and its connection.
//
// Lifecycle:  StartClient -> Initialize -> (use Conn) -> Shutdown.
// Shutdown is always safe to call, also after the server died.
type Client struct {
	opts Options
	cmd  *exec.Cmd
	conn *Conn

	exited  chan struct{} // closed after cmd.Wait returned
	waitErr error         // valid once exited is closed

	mu         sync.Mutex
	state      clientState
	caps       ServerCapabilities
	serverName string
}

// StartClient starts the server process and the read loop. It does not
// talk to the server yet; call Initialize next.
func StartClient(opts Options) (*Client, error) {
	if opts.Server.Command == "" {
		return nil, errors.New("lsp: empty server command")
	}
	if opts.Name == "" {
		opts.Name = filepath.Base(opts.Server.Command)
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 3 * time.Second
	}

	cmd := exec.Command(opts.Server.Command, opts.Server.Args...)
	cmd.Dir = opts.Server.Dir
	if cmd.Dir == "" {
		cmd.Dir = opts.RootPath
	}
	cmd.Env = append(os.Environ(), opts.Server.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: starting %s: %w", opts.Server.Command, err)
	}

	c := &Client{
		opts:   opts,
		cmd:    cmd,
		exited: make(chan struct{}),
	}
	c.conn = NewConn(stdout, stdin, Handler{
		Request:      c.handleRequest,
		Notification: c.handleNotification,
	})
	c.conn.Start()

	stderrDone := make(chan struct{})
	go c.drainStderr(stderr, stderrDone)
	go c.reap(stderrDone)

	return c, nil
}

func (c *Client) logf(format string, args ...any) {
	if c.opts.Logf != nil {
		c.opts.Logf(format, args...)
	}
}

// drainStderr forwards server stderr line by line. It must run until EOF:
// a server blocked on a full stderr pipe would stop answering.
func (c *Client) drainStderr(r io.Reader, done chan<- struct{}) {
	defer close(done)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		c.logf("[%s stderr] %s", c.opts.Name, sc.Text())
	}
	// A line longer than the buffer stops the Scanner; keep draining so the
	// server does not block.
	if sc.Err() != nil {
		buf := make([]byte, 32<<10)
		for {
			if _, err := r.Read(buf); err != nil {
				return
			}
		}
	}
}

// reap waits for the process. exec.Cmd.Wait must not run before all reads
// from the pipes finished, so it first waits for stdout EOF (the read loop
// ending) and stderr EOF. A server that closes stdout but keeps running is
// killed after ShutdownTimeout.
func (c *Client) reap(stderrDone <-chan struct{}) {
	<-c.conn.Done()
	t := time.AfterFunc(c.opts.ShutdownTimeout, c.kill)
	<-stderrDone
	c.waitErr = c.cmd.Wait()
	t.Stop()
	close(c.exited)
}

func (c *Client) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// Conn returns the JSON-RPC connection for requests and notifications.
func (c *Client) Conn() *Conn { return c.conn }

// Done is closed when the server process has exited and been reaped.
func (c *Client) Done() <-chan struct{} { return c.exited }

// Ready reports whether Initialize has completed and Shutdown has not begun.
func (c *Client) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == stateReady
}

// Capabilities returns what the server announced. Zero before Initialize.
func (c *Client) Capabilities() ServerCapabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// ServerName is serverInfo.name, or "" if the server did not send one.
func (c *Client) ServerName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverName
}

type initializeParams struct {
	ProcessID             int               `json:"processId"`
	ClientInfo            clientInfo        `json:"clientInfo"`
	RootURI               *string           `json:"rootUri"` // required; null = no root
	InitializationOptions any               `json:"initializationOptions,omitempty"`
	Capabilities          map[string]any    `json:"capabilities"`
	WorkspaceFolders      []workspaceFolder `json:"workspaceFolders,omitempty"`
}

type clientInfo struct {
	Name string `json:"name"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type initializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *struct {
		Name string `json:"name"`
	} `json:"serverInfo"`
}

// Initialize performs the initialize request and the initialized
// notification. The client must not send anything else before it returns.
func (c *Client) Initialize(ctx context.Context) error {
	c.mu.Lock()
	if c.state != stateStarted {
		c.mu.Unlock()
		return errors.New("lsp: Initialize called twice or after Shutdown")
	}
	c.mu.Unlock()

	params := initializeParams{
		ProcessID:             os.Getpid(),
		ClientInfo:            clientInfo{Name: "ge"},
		InitializationOptions: c.opts.InitializationOptions,
		Capabilities:          defaultClientCapabilities(),
	}
	if root := c.opts.RootPath; root != "" {
		uri := FileURI(root)
		params.RootURI = &uri
		params.WorkspaceFolders = []workspaceFolder{{URI: uri, Name: filepath.Base(root)}}
	}

	var res initializeResult
	if err := c.conn.Call(ctx, "initialize", params, &res); err != nil {
		return fmt.Errorf("lsp: initialize: %w", err)
	}
	if err := c.conn.Notify("initialized", struct{}{}); err != nil {
		return fmt.Errorf("lsp: initialized: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != stateStarted { // Shutdown raced with us
		return errors.New("lsp: shut down during initialize")
	}
	c.caps = res.Capabilities
	if res.ServerInfo != nil {
		c.serverName = res.ServerInfo.Name
	}
	c.state = stateReady
	return nil
}

// Shutdown stops the server: shutdown request, exit notification, then
// waits for the process, killing it if a step exceeds ShutdownTimeout or
// ctx ends. It returns once the process is gone.
//
// A server that was never initialized is killed right away (the protocol
// has no graceful path before initialize). Concurrent or repeated calls
// just wait for the first one's result.
func (c *Client) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	wasReady := c.state == stateReady
	already := c.state == stateStopping
	c.state = stateStopping
	c.mu.Unlock()

	if already {
		return c.waitExited(ctx)
	}

	var firstErr error
	note := func(err error) {
		if err != nil && !errors.Is(err, ErrClosed) && firstErr == nil {
			firstErr = err
		}
	}

	killed := false
	if wasReady {
		sctx, cancel := context.WithTimeout(ctx, c.opts.ShutdownTimeout)
		note(c.conn.Call(sctx, "shutdown", nil, nil))
		cancel()
		note(c.conn.Notify("exit", nil))

		timer := time.NewTimer(c.opts.ShutdownTimeout)
		select {
		case <-c.exited:
		case <-timer.C:
			killed = true
		case <-ctx.Done():
			killed = true
		}
		timer.Stop()
	} else {
		killed = true
	}

	if killed {
		c.kill()
	}

	// After a kill the process is gone almost at once; bound the wait anyway.
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.waitExited(wctx); err != nil {
		return errors.Join(firstErr, err)
	}

	switch {
	case killed && wasReady && firstErr == nil:
		firstErr = errors.New("lsp: server did not exit in time and was killed")
	case !killed && c.waitErr != nil && firstErr == nil:
		firstErr = fmt.Errorf("lsp: server exited with error: %w", c.waitErr)
	}
	return firstErr
}

func (c *Client) waitExited(ctx context.Context) error {
	select {
	case <-c.exited:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("lsp: waiting for server to exit: %w", ctx.Err())
	}
}

// handleRequest answers the requests every client must answer, then
// delegates to Options.Handler.
func (c *Client) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "client/registerCapability", "client/unregisterCapability",
		"window/workDoneProgress/create", "window/showMessageRequest":
		// Accepted and ignored: ge announces no dynamic registration, so
		// this only happens for optional extras.
		return nil, nil

	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		return make([]any, len(p.Items)), nil // one null per item: "no settings"
	}

	if h := c.opts.Handler.Request; h != nil {
		return h(ctx, method, params)
	}
	return nil, &ResponseError{
		Code:    CodeMethodNotFound,
		Message: "method not supported by client: " + method,
	}
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	if method == "window/logMessage" {
		var p struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &p) == nil {
			c.logf("[%s] %s", c.opts.Name, p.Message)
		}
	}
	if h := c.opts.Handler.Notification; h != nil {
		h(method, params)
	}
}
