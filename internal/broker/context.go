package broker

import "context"

type ctxKey struct{}

// EscalationState is shared between broker middleware via request context.
type EscalationState struct {
	CycleID    string
	Triggered  bool
	Signal     EscalationSignal
	Permission *Permission
	Shaped     *ShaperResult
}

func getState(ctx context.Context) *EscalationState {
	if s, ok := ctx.Value(ctxKey{}).(*EscalationState); ok {
		return s
	}
	return nil
}

func withState(ctx context.Context, s *EscalationState) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}
