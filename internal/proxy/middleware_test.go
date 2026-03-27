package proxy

import (
	"context"
	"testing"

	"aibroker/internal/jsonrpc"
)

func TestPipeline(t *testing.T) {
	ctx := context.Background()
	var order []string

	final := func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) {
		order = append(order, "final")
		return nil, nil
	}
	mwA := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			order = append(order, "A")
			return next(ctx, msg)
		}
	}
	mwB := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			order = append(order, "B")
			return next(ctx, msg)
		}
	}

	h := Pipeline(final, mwA, mwB)
	if _, err := h(ctx, &jsonrpc.Message{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"A", "B", "final"}
	assertStringSliceEqual(t, order, want)
}

func TestWhen(t *testing.T) {
	ctx := context.Background()
	var ran bool
	mw := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			ran = true
			return next(ctx, msg)
		}
	}
	h := Pipeline(
		func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) { return nil, nil },
		When(func(*jsonrpc.Message) bool { return true }, mw),
	)
	if _, err := h(ctx, &jsonrpc.Message{}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("expected middleware when predicate true")
	}

	ran = false
	h = Pipeline(
		func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) { return nil, nil },
		When(func(*jsonrpc.Message) bool { return false }, mw),
	)
	if _, err := h(ctx, &jsonrpc.Message{}); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("expected middleware skipped when predicate false")
	}
}

func TestBranch(t *testing.T) {
	ctx := context.Background()
	var branch string
	trueMW := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			branch = "true"
			return next(ctx, msg)
		}
	}
	falseMW := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			branch = "false"
			return next(ctx, msg)
		}
	}
	final := func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) { return nil, nil }

	h := Pipeline(final, Branch(func(*jsonrpc.Message) bool { return true }, trueMW, falseMW))
	if _, err := h(ctx, &jsonrpc.Message{}); err != nil {
		t.Fatal(err)
	}
	if branch != "true" {
		t.Fatalf("got %q", branch)
	}

	h = Pipeline(final, Branch(func(*jsonrpc.Message) bool { return false }, trueMW, falseMW))
	if _, err := h(ctx, &jsonrpc.Message{}); err != nil {
		t.Fatal(err)
	}
	if branch != "false" {
		t.Fatalf("got %q", branch)
	}
}

func TestRouter(t *testing.T) {
	ctx := context.Background()
	var keySeen string
	routeA := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			keySeen = "a"
			return next(ctx, msg)
		}
	}
	fallback := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			keySeen = "fb"
			return next(ctx, msg)
		}
	}
	final := func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) { return nil, nil }

	classify := func(msg *jsonrpc.Message) string { return msg.Method }
	routes := map[string]Middleware{"x": routeA}

	h := Pipeline(final, Router(classify, routes, fallback))
	if _, err := h(ctx, &jsonrpc.Message{Method: "x"}); err != nil {
		t.Fatal(err)
	}
	if keySeen != "a" {
		t.Fatalf("route: got %q", keySeen)
	}

	if _, err := h(ctx, &jsonrpc.Message{Method: "other"}); err != nil {
		t.Fatal(err)
	}
	if keySeen != "fb" {
		t.Fatalf("fallback: got %q", keySeen)
	}

	var hitFinal int
	finalOnly := func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) {
		hitFinal++
		return nil, nil
	}
	h = Pipeline(finalOnly, Router(classify, routes, nil))
	keySeen = ""
	if _, err := h(ctx, &jsonrpc.Message{Method: "missing"}); err != nil {
		t.Fatal(err)
	}
	if keySeen != "" {
		t.Fatalf("unexpected route activity: %q", keySeen)
	}
	if hitFinal != 1 {
		t.Fatalf("final calls: %d", hitFinal)
	}
}

func TestCompose(t *testing.T) {
	ctx := context.Background()
	var order []string
	final := func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) {
		order = append(order, "final")
		return nil, nil
	}
	mw1 := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			order = append(order, "1")
			return next(ctx, msg)
		}
	}
	mw2 := func(next Handler) Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			order = append(order, "2")
			return next(ctx, msg)
		}
	}

	composed := Compose(mw1, mw2)
	h := composed(final)
	if _, err := h(ctx, &jsonrpc.Message{}); err != nil {
		t.Fatal(err)
	}
	assertStringSliceEqual(t, order, []string{"1", "2", "final"})
}

func TestPassthrough(t *testing.T) {
	ctx := context.Background()
	called := false
	final := func(context.Context, *jsonrpc.Message) (*jsonrpc.Message, error) {
		called = true
		return nil, nil
	}
	h := Passthrough()(final)
	if _, err := h(ctx, &jsonrpc.Message{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("final not called")
	}
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len %d want %d: got %#v want %#v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q want %q (full got %#v)", i, got[i], want[i], got)
		}
	}
}
