package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTimeout = 2 * time.Second

// newPair connects two Conns back to back. kill makes both sides see EOF.
func newPair(t *testing.T, ha, hb Handler) (a, b *Conn, kill func()) {
	t.Helper()

	ar, bw := io.Pipe() // b writes, a reads
	br, aw := io.Pipe() // a writes, b reads

	a = NewConn(ar, aw, ha)
	b = NewConn(br, bw, hb)
	a.Start()
	b.Start()

	var once sync.Once
	kill = func() {
		once.Do(func() {
			bw.Close()
			aw.Close()
		})
	}
	t.Cleanup(kill)
	return a, b, kill
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	for _, body := range []string{`{"a":1}`, ``, `{"s":"あ😀"}`} {
		if err := writeFrame(&buf, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	r := bufio.NewReader(&buf)
	for _, want := range []string{`{"a":1}`, ``, `{"s":"あ😀"}`} {
		got, err := readFrame(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if _, err := readFrame(r); err != io.EOF {
		t.Fatalf("want io.EOF between messages, got %v", err)
	}
}

func TestReadFrameHeaders(t *testing.T) {
	// Case-insensitive name, extra Content-Type header.
	in := "content-length: 2\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n{}"
	got, err := readFrame(bufio.NewReader(strings.NewReader(in)))
	if err != nil || string(got) != "{}" {
		t.Fatalf("got %q, %v", got, err)
	}

	for name, in := range map[string]string{
		"missing length":  "Content-Type: x\r\n\r\n{}",
		"bad length":      "Content-Length: abc\r\n\r\n",
		"negative length": "Content-Length: -1\r\n\r\n",
		"malformed line":  "Content-Length 2\r\n\r\n{}",
	} {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestReadFrameTruncated(t *testing.T) {
	for name, in := range map[string]string{
		"short body":   "Content-Length: 10\r\n\r\n{}",
		"no body":      "Content-Length: 10\r\n\r\n",
		"mid headers":  "Content-Length: 10\r\n",
		"partial line": "Content-Len",
	} {
		_, err := readFrame(bufio.NewReader(strings.NewReader(in)))
		if err != io.ErrUnexpectedEOF {
			t.Errorf("%s: got %v, want io.ErrUnexpectedEOF", name, err)
		}
	}
}

func TestCallResult(t *testing.T) {
	a, _, _ := newPair(t, Handler{}, Handler{
		Request: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != "add" {
				t.Errorf("method = %q", method)
			}
			var p [2]int
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			return p[0] + p[1], nil
		},
	})

	var sum int
	if err := a.Call(context.Background(), "add", [2]int{2, 3}, &sum); err != nil {
		t.Fatal(err)
	}
	if sum != 5 {
		t.Fatalf("sum = %d", sum)
	}
}

func TestCallNullResult(t *testing.T) {
	a, _, _ := newPair(t, Handler{}, Handler{
		Request: func(context.Context, string, json.RawMessage) (any, error) { return nil, nil },
	})

	v := 42
	if err := a.Call(context.Background(), "m", nil, &v); err != nil {
		t.Fatal(err)
	}
	if v != 42 {
		t.Fatalf("null result must leave the target untouched, got %d", v)
	}
}

func TestCallError(t *testing.T) {
	a, _, _ := newPair(t, Handler{}, Handler{
		Request: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case "typed":
				return nil, &ResponseError{Code: CodeContentModified, Message: "stale"}
			default:
				return nil, errors.New("boom")
			}
		},
	})

	var re *ResponseError
	err := a.Call(context.Background(), "typed", nil, nil)
	if !errors.As(err, &re) || re.Code != CodeContentModified {
		t.Fatalf("typed: %v", err)
	}

	err = a.Call(context.Background(), "plain", nil, nil)
	if !errors.As(err, &re) || re.Code != CodeInternalError || re.Message != "boom" {
		t.Fatalf("plain: %v", err)
	}
}

func TestRequestWithoutHandlerIsMethodNotFound(t *testing.T) {
	a, _, _ := newPair(t, Handler{}, Handler{})

	var re *ResponseError
	err := a.Call(context.Background(), "workspace/configuration", nil, nil)
	if !errors.As(err, &re) || re.Code != CodeMethodNotFound {
		t.Fatalf("got %v", err)
	}
}

func TestNotificationOrder(t *testing.T) {
	const n = 100
	got := make(chan string, n)
	a, _, _ := newPair(t, Handler{}, Handler{
		Notification: func(method string, params json.RawMessage) { got <- string(params) },
	})

	for i := 0; i < n; i++ {
		b, _ := json.Marshal(i)
		if err := a.Notify("n", json.RawMessage(b)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		select {
		case s := <-got:
			b, _ := json.Marshal(i)
			if s != string(b) {
				t.Fatalf("notification %d arrived as %s", i, s)
			}
		case <-time.After(testTimeout):
			t.Fatalf("timed out at %d", i)
		}
	}
}

func TestConcurrentCalls(t *testing.T) {
	a, _, _ := newPair(t, Handler{}, Handler{
		Request: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
			var n int
			_ = json.Unmarshal(params, &n)
			return n, nil
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out int
			if err := a.Call(context.Background(), "echo", i, &out); err != nil {
				t.Errorf("call %d: %v", i, err)
				return
			}
			if out != i {
				t.Errorf("call %d got %d", i, out)
			}
		}(i)
	}
	wg.Wait()
}

func TestBothDirections(t *testing.T) {
	// The server (b) calls back into the client (a) while a's own call is
	// still waiting, like workspace/configuration during initialize.
	var b *Conn
	a, b, _ := newPair(t,
		Handler{
			Request: func(context.Context, string, json.RawMessage) (any, error) { return "cfg", nil },
		},
		Handler{
			Request: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
				var s string
				if err := b.Call(ctx, "workspace/configuration", nil, &s); err != nil {
					return nil, err
				}
				return "init:" + s, nil
			},
		})

	var out string
	if err := a.Call(context.Background(), "initialize", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out != "init:cfg" {
		t.Fatalf("out = %q", out)
	}
}

func TestCancelSendsCancelRequest(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})

	a, _, _ := newPair(t, Handler{}, Handler{
		Request: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, &ResponseError{Code: CodeRequestCancelled, Message: "cancelled"}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- a.Call(ctx, "slow", nil, nil) }()

	waitFor(t, started, "request to reach the server")
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Call did not return after cancel")
	}

	waitFor(t, cancelled, "server-side context to be cancelled by $/cancelRequest")

	// The late CodeRequestCancelled response must be dropped quietly:
	// the connection stays open and nothing is left pending.
	time.Sleep(50 * time.Millisecond)
	if err := a.Err(); err != nil {
		t.Fatalf("connection broke after cancel: %v", err)
	}
	a.mu.Lock()
	n := len(a.pending)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d pending entries left after cancel", n)
	}
}

func TestCloseFailsPendingCalls(t *testing.T) {
	started := make(chan struct{})
	a, b, kill := newPair(t, Handler{}, Handler{
		Request: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})

	errc := make(chan error, 1)
	go func() { errc <- a.Call(context.Background(), "hang", nil, nil) }()
	waitFor(t, started, "request to reach the server")

	kill()

	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("got %v, want ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("pending Call was not failed on close")
	}

	waitFor(t, a.Done(), "a.Done")
	waitFor(t, b.Done(), "b.Done")

	if err := a.Call(context.Background(), "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Call after close: %v", err)
	}
	if err := a.Notify("x", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Notify after close: %v", err)
	}
}
