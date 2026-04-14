package providers

import (
	"context"
	"errors"
	"sync/atomic"
)

// RoundRobinProvider wraps multiple Provider instances (same backend, different API keys)
// and rotates between them. On a 429 rate-limit response the next key is tried immediately.
type RoundRobinProvider struct {
	name     string
	backends []Provider
	index    atomic.Int64
}

// NewRoundRobinProvider creates a round-robin wrapper over the supplied backends.
func NewRoundRobinProvider(name string, backends []Provider) *RoundRobinProvider {
	return &RoundRobinProvider{name: name, backends: backends}
}

func (r *RoundRobinProvider) Name() string        { return r.name }
func (r *RoundRobinProvider) DefaultModel() string { return r.backends[0].DefaultModel() }

func (r *RoundRobinProvider) SupportsThinking() bool {
	if tc, ok := r.backends[0].(ThinkingCapable); ok {
		return tc.SupportsThinking()
	}
	return false
}

func (r *RoundRobinProvider) pick() (Provider, int64) {
	n := int64(len(r.backends))
	idx := r.index.Load() % n
	return r.backends[idx], idx
}

func (r *RoundRobinProvider) rotate(from int64) {
	n := int64(len(r.backends))
	r.index.CompareAndSwap(from, (from+1)%n)
}

func (r *RoundRobinProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	b, idx := r.pick()
	resp, err := b.Chat(ctx, req)
	if err != nil && is429(err) {
		r.rotate(idx)
		next, _ := r.pick()
		return next.Chat(ctx, req)
	}
	return resp, err
}

func (r *RoundRobinProvider) ChatStream(ctx context.Context, req ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error) {
	b, idx := r.pick()
	resp, err := b.ChatStream(ctx, req, onChunk)
	if err != nil && is429(err) {
		r.rotate(idx)
		next, _ := r.pick()
		return next.ChatStream(ctx, req, onChunk)
	}
	return resp, err
}

func is429(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.Status == 429
}
