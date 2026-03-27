package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"aibroker/internal/jsonrpc"
	"aibroker/internal/transport"
)

type Direction int

const (
	Inbound  Direction = iota // frontend → backend
	Outbound                  // backend → frontend
)

type directionKey struct{}

func WithDirection(ctx context.Context, d Direction) context.Context {
	return context.WithValue(ctx, directionKey{}, d)
}

func ContextWithDirection(ctx context.Context, d Direction) context.Context {
	return WithDirection(ctx, d)
}

func DirectionFromContext(ctx context.Context) (Direction, bool) {
	d, ok := ctx.Value(directionKey{}).(Direction)
	return d, ok
}

type Proxy struct {
	frontend transport.Transport
	backend  transport.Transport
	pipeline Middleware
	logger   *slog.Logger
}

func NewProxy(frontend, backend transport.Transport, pipeline Middleware, logger *slog.Logger) *Proxy {
	return &Proxy{
		frontend: frontend,
		backend:  backend,
		pipeline: pipeline,
		logger:   logger,
	}
}

func (p *Proxy) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		_ = p.frontend.Close()
		_ = p.backend.Close()
	}()

	inCh := make(chan error, 1)
	outCh := make(chan error, 1)

	go func() { inCh <- p.relay(ctx, Inbound, p.frontend, p.backend) }()
	go func() { outCh <- p.relay(ctx, Outbound, p.backend, p.frontend) }()

	select {
	case err := <-inCh:
		// Frontend done (editor closed stdin).
		// Close agent's stdin so it can finish processing and exit.
		if cw, ok := p.backend.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		return coalesce(<-outCh, err)

	case err := <-outCh:
		// Backend done (agent exited). Unblock inbound relay.
		cancel()
		_ = p.frontend.Close()
		<-inCh
		return err

	case <-ctx.Done():
		_ = p.frontend.Close()
		_ = p.backend.Close()
		<-inCh
		<-outCh
		return ctx.Err()
	}
}

func (p *Proxy) relay(ctx context.Context, dir Direction, src, dst transport.Transport) error {
	mw := p.pipeline
	if mw == nil {
		mw = Passthrough()
	}
	identity := func(_ context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
		return msg, nil
	}
	h := mw(identity)

	for {
		msg, err := src.Read()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}

		out, err := h(WithDirection(ctx, dir), msg)
		if err != nil {
			return err
		}
		if out != nil {
			if err := dst.Write(out); err != nil {
				return err
			}
		}
	}
}

// coalesce returns the first non-nil error.
func coalesce(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
