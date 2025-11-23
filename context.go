package imprint

import "context"

type contextKey struct{}

var activeSpanKey = contextKey{}

// NewContext returns a new context with the given span attached.
func NewContext(ctx context.Context, span *Span) context.Context {
	return context.WithValue(ctx, activeSpanKey, span)
}

// FromContext returns the active span from the context, or nil if none exists.
func FromContext(ctx context.Context) *Span {
	val := ctx.Value(activeSpanKey)
	if span, ok := val.(*Span); ok {
		return span
	}
	return nil
}
