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

var suppressTracingKey = contextKey{}

// SuppressTracing returns a new context that suppresses tracing.
// Any spans created with this context will be no-ops.
func SuppressTracing(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressTracingKey, true)
}

// IsSuppressed returns true if tracing is suppressed in the context.
func IsSuppressed(ctx context.Context) bool {
	val, ok := ctx.Value(suppressTracingKey).(bool)
	return ok && val
}
