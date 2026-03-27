package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"aibroker/internal/jsonrpc"
	"aibroker/internal/transport"
)

type chanTransport struct {
	in             chan *jsonrpc.Message
	out            chan *jsonrpc.Message
	closed         chan struct{}
	closeOnce      sync.Once
	closeWriteOnce sync.Once
}

func newChanTransport(buf int) *chanTransport {
	return &chanTransport{
		in:     make(chan *jsonrpc.Message, buf),
		out:    make(chan *jsonrpc.Message, buf),
		closed: make(chan struct{}),
	}
}

func (t *chanTransport) Read() (*jsonrpc.Message, error) {
	select {
	case <-t.closed:
		return nil, io.EOF
	case msg, ok := <-t.in:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
}

func (t *chanTransport) Write(msg *jsonrpc.Message) error {
	select {
	case <-t.closed:
		return io.ErrClosedPipe
	case t.out <- msg:
		return nil
	}
}

func (t *chanTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)
	})
	return nil
}

func (t *chanTransport) CloseWrite() error {
	t.closeWriteOnce.Do(func() {
		close(t.in)
	})
	return nil
}

var (
	_ transport.Transport             = (*chanTransport)(nil)
	_ interface{ CloseWrite() error } = (*chanTransport)(nil)
)

func TestProxyBidirectional(t *testing.T) {
	fe := newChanTransport(8)
	be := newChanTransport(8)
	errCh := make(chan error, 1)
	go func() {
		p := NewProxy(fe, be, nil, discardLogger())
		errCh <- p.Run(context.Background())
	}()

	req := &jsonrpc.Message{
		JSONRPC: jsonrpc.Version,
		ID:      json.RawMessage(`1`),
		Method:  "call",
		Params:  json.RawMessage(`{"a":1}`),
	}
	fe.in <- req
	gotDown, err := recvTimeout(t, be.out, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertMsgJSONEqual(t, req, gotDown)

	resp := &jsonrpc.Message{
		JSONRPC: jsonrpc.Version,
		ID:      json.RawMessage(`1`),
		Result:  json.RawMessage(`"ok"`),
	}
	be.in <- resp
	gotUp, err := recvTimeout(t, fe.out, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertMsgJSONEqual(t, resp, gotUp)

	_ = fe.Close()
	waitProxyDone(t, errCh, time.Second)
}

func TestProxyMiddleware(t *testing.T) {
	fe := newChanTransport(8)
	be := newChanTransport(8)
	tag := Middleware(func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			cp := *msg
			cp.Params = json.RawMessage(`{"mw":true}`)
			return next(ctx, &cp)
		}
	})
	errCh := make(chan error, 1)
	go func() {
		p := NewProxy(fe, be, tag, discardLogger())
		errCh <- p.Run(context.Background())
	}()

	req := &jsonrpc.Message{
		JSONRPC: jsonrpc.Version,
		ID:      json.RawMessage(`7`),
		Method:  "m",
		Params:  json.RawMessage(`{}`),
	}
	fe.in <- req
	got, err := recvTimeout(t, be.out, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := &jsonrpc.Message{
		JSONRPC: jsonrpc.Version,
		ID:      json.RawMessage(`7`),
		Method:  "m",
		Params:  json.RawMessage(`{"mw":true}`),
	}
	assertMsgJSONEqual(t, want, got)
	_ = fe.Close()
	waitProxyDone(t, errCh, time.Second)
}

func TestProxyFrontendEOF(t *testing.T) {
	fe := newChanTransport(8)
	be := newChanTransport(8)
	errCh := make(chan error, 1)
	go func() {
		p := NewProxy(fe, be, nil, discardLogger())
		errCh <- p.Run(context.Background())
	}()
	_ = fe.Close()
	waitProxyDone(t, errCh, time.Second)
}

func TestProxyBackendEOF(t *testing.T) {
	closedIn := make(chan *jsonrpc.Message)
	close(closedIn)
	be := &chanTransport{
		in:     closedIn,
		out:    make(chan *jsonrpc.Message, 8),
		closed: make(chan struct{}),
	}
	fe := newChanTransport(8)
	errCh := make(chan error, 1)
	go func() {
		p := NewProxy(fe, be, nil, discardLogger())
		errCh <- p.Run(context.Background())
	}()
	waitProxyDone(t, errCh, time.Second)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func recvTimeout(t *testing.T, ch <-chan *jsonrpc.Message, d time.Duration) (*jsonrpc.Message, error) {
	t.Helper()
	select {
	case m := <-ch:
		return m, nil
	case <-time.After(d):
		return nil, errors.New("timeout waiting for message")
	}
}

func waitProxyDone(t *testing.T, errCh <-chan error, d time.Duration) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("proxy Run: %v", err)
		}
	case <-time.After(d):
		t.Fatal("timeout waiting for proxy")
	}
}

func assertMsgJSONEqual(t *testing.T, want, got *jsonrpc.Message) {
	t.Helper()
	wj, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	gj, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(wj) != string(gj) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", gj, wj)
	}
}
