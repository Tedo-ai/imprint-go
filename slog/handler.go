// Package slog provides a slog.Handler that automatically injects trace context
// into log records, enabling log correlation with distributed traces.
//
// Usage:
//
//	import (
//	    "log/slog"
//	    "os"
//	    imprint "github.com/tedo-ai/imprint-go"
//	    imprintslog "github.com/tedo-ai/imprint-go/slog"
//	)
//
//	// Create a traced slog handler wrapping a JSON handler
//	handler := imprintslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil))
//	logger := slog.New(handler)
//
//	// In a request handler with trace context:
//	func handleRequest(w http.ResponseWriter, r *http.Request) {
//	    ctx := r.Context() // Contains active span from middleware
//	    logger.InfoContext(ctx, "Processing request", "user_id", 123)
//	    // Output includes: "trace_id": "abc123...", "span_id": "def456..."
//	}
package slog

import (
	"context"
	"io"
	"log/slog"

	imprint "github.com/tedo-ai/imprint-go"
)

// Handler wraps a slog.Handler and injects trace context attributes
// (trace_id, span_id) into log records when an active span exists in the context.
type Handler struct {
	parent slog.Handler
	opts   HandlerOptions
}

// HandlerOptions configures the trace-aware slog handler.
type HandlerOptions struct {
	// TraceIDKey is the attribute key for the trace ID.
	// Defaults to "trace_id".
	TraceIDKey string

	// SpanIDKey is the attribute key for the span ID.
	// Defaults to "span_id".
	SpanIDKey string

	// AddTraceParent adds the full W3C traceparent string as an attribute.
	// Defaults to false.
	AddTraceParent bool

	// TraceParentKey is the attribute key for the traceparent.
	// Only used if AddTraceParent is true. Defaults to "traceparent".
	TraceParentKey string
}

// DefaultHandlerOptions returns sensible defaults for the handler.
func DefaultHandlerOptions() HandlerOptions {
	return HandlerOptions{
		TraceIDKey:     "trace_id",
		SpanIDKey:      "span_id",
		AddTraceParent: false,
		TraceParentKey: "traceparent",
	}
}

// NewHandler creates a new trace-aware slog handler that wraps another handler.
// It automatically adds trace_id and span_id attributes when an active span
// exists in the context.
func NewHandler(parent slog.Handler) *Handler {
	return NewHandlerWithOptions(parent, DefaultHandlerOptions())
}

// NewHandlerWithOptions creates a new trace-aware slog handler with custom options.
func NewHandlerWithOptions(parent slog.Handler, opts HandlerOptions) *Handler {
	if opts.TraceIDKey == "" {
		opts.TraceIDKey = "trace_id"
	}
	if opts.SpanIDKey == "" {
		opts.SpanIDKey = "span_id"
	}
	if opts.TraceParentKey == "" {
		opts.TraceParentKey = "traceparent"
	}
	return &Handler{
		parent: parent,
		opts:   opts,
	}
}

// NewJSONHandler is a convenience function that creates a trace-aware JSON handler
// writing to the given writer.
func NewJSONHandler(w io.Writer, opts *slog.HandlerOptions) *Handler {
	return NewHandler(slog.NewJSONHandler(w, opts))
}

// NewTextHandler is a convenience function that creates a trace-aware text handler
// writing to the given writer.
func NewTextHandler(w io.Writer, opts *slog.HandlerOptions) *Handler {
	return NewHandler(slog.NewTextHandler(w, opts))
}

// Enabled reports whether the handler is enabled for the given level.
// Delegates to the parent handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.parent.Enabled(ctx, level)
}

// Handle processes the log record, adding trace context if available.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	// Check for active span in context
	span := imprint.FromContext(ctx)
	if span != nil {
		// Add trace context attributes
		record.AddAttrs(
			slog.String(h.opts.TraceIDKey, span.TraceID),
			slog.String(h.opts.SpanIDKey, span.SpanID),
		)

		// Optionally add full traceparent
		if h.opts.AddTraceParent {
			record.AddAttrs(slog.String(h.opts.TraceParentKey, span.TraceParentString()))
		}
	}

	return h.parent.Handle(ctx, record)
}

// WithAttrs returns a new handler with the given attributes added.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		parent: h.parent.WithAttrs(attrs),
		opts:   h.opts,
	}
}

// WithGroup returns a new handler with the given group name.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		parent: h.parent.WithGroup(name),
		opts:   h.opts,
	}
}

// LoggerFromContext returns a logger that will automatically include trace context
// from the given context. This is useful when you want to pass around a logger
// that's already bound to a trace.
func LoggerFromContext(ctx context.Context, logger *slog.Logger) *slog.Logger {
	span := imprint.FromContext(ctx)
	if span == nil {
		return logger
	}

	return logger.With(
		slog.String("trace_id", span.TraceID),
		slog.String("span_id", span.SpanID),
	)
}
