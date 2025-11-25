package imprint

import (
	"context"
	"strings"
)

// TraceparentKey is the standard key for W3C trace context propagation.
const TraceparentKey = "traceparent"

// Inject writes the trace context from the given context into the carrier map.
// This is useful for propagating trace context across async boundaries like:
// - Message queues (Redis, RabbitMQ, Kafka)
// - Event buses
// - Background job payloads
// - Custom RPC systems
//
// The carrier map will contain:
// - "traceparent": W3C trace context string (e.g., "00-traceid-spanid-01")
//
// Example usage with a job queue:
//
//	func EnqueueJob(ctx context.Context, jobData map[string]interface{}) {
//	    carrier := make(map[string]string)
//	    imprint.Inject(ctx, carrier)
//	    jobData["_trace"] = carrier
//	    queue.Push(jobData)
//	}
func Inject(ctx context.Context, carrier map[string]string) {
	span := FromContext(ctx)
	if span == nil {
		return
	}

	carrier[TraceparentKey] = span.TraceParentString()
}

// Extract reads trace context from the carrier map and returns a new context
// with the parent span information attached. This allows child spans created
// from the returned context to be linked to the original trace.
//
// Example usage in a job worker:
//
//	func ProcessJob(jobData map[string]interface{}) {
//	    carrier := jobData["_trace"].(map[string]string)
//	    ctx := imprint.Extract(carrier)
//	    ctx, span := client.StartSpan(ctx, "process_job")
//	    defer span.End()
//	    // ... process job
//	}
func Extract(carrier map[string]string) context.Context {
	return ExtractWithContext(context.Background(), carrier)
}

// ExtractWithContext is like Extract but allows you to specify a base context.
// The trace parent information is added to the provided context.
func ExtractWithContext(ctx context.Context, carrier map[string]string) context.Context {
	traceparent := carrier[TraceparentKey]
	if traceparent == "" {
		return ctx
	}

	// Parse W3C traceparent: version-traceid-spanid-flags
	parts := strings.Split(traceparent, "-")
	if len(parts) != 4 {
		return ctx
	}

	traceID := parts[1]
	spanID := parts[2]

	// Validate lengths (trace_id should be 32 hex chars, span_id 16 hex chars)
	if len(traceID) != 32 || len(spanID) != 16 {
		return ctx
	}

	// Create a synthetic parent span to hold the context
	parentSpan := &Span{
		TraceID: traceID,
		SpanID:  spanID,
	}

	return NewContext(ctx, parentSpan)
}

// InjectToHeaders is a convenience function that injects trace context
// directly into an http.Header-like map[string][]string.
// This is useful when you need to propagate context to HTTP-style interfaces.
func InjectToHeaders(ctx context.Context, headers map[string][]string) {
	span := FromContext(ctx)
	if span == nil {
		return
	}

	headers[TraceparentKey] = []string{span.TraceParentString()}
}

// ExtractFromHeaders extracts trace context from an http.Header-like map.
func ExtractFromHeaders(headers map[string][]string) context.Context {
	return ExtractFromHeadersWithContext(context.Background(), headers)
}

// ExtractFromHeadersWithContext is like ExtractFromHeaders but with a base context.
func ExtractFromHeadersWithContext(ctx context.Context, headers map[string][]string) context.Context {
	values := headers[TraceparentKey]
	if len(values) == 0 {
		// Try lowercase (headers are often normalized)
		values = headers["Traceparent"]
	}
	if len(values) == 0 {
		return ctx
	}

	carrier := map[string]string{
		TraceparentKey: values[0],
	}
	return ExtractWithContext(ctx, carrier)
}
