package imprint

import (
	"context"
	"log/slog"
)

type traceHandler struct {
	next slog.Handler
}

// NewSlogHandler wraps next and injects trace_id and span_id into every log
// record when an active Imprint span exists in the context. It is a no-op
// when no span is active — safe to use unconditionally.
func NewSlogHandler(next slog.Handler) slog.Handler {
	return &traceHandler{next: next}
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if span := FromContext(ctx); span != nil {
		r.AddAttrs(
			slog.String("trace_id", span.TraceID),
			slog.String("span_id", span.SpanID),
		)
	}
	return h.next.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{next: h.next.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{next: h.next.WithGroup(name)}
}
