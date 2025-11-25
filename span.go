package imprint

import (
	"fmt"
	"sync"
	"time"
)

// Span represents a single operation within a trace.
type Span struct {
	TraceID    string    `json:"trace_id"`
	SpanID     string    `json:"span_id"`
	ParentID   *string   `json:"parent_id,omitempty"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	StartTime  time.Time `json:"start_time"`
	DurationNS uint64    `json:"duration_ns"`
	StatusCode uint16    `json:"status_code"`
	ErrorData  string            `json:"error_data,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`

	mu       sync.Mutex
	client   *Client
	sampled  bool // whether this span should be sent to the backend
	promoted bool // whether this span was promoted due to error (tail-based)
	isRoot   bool // whether this is a root span (no parent)
}

// End marks the span as completed and queues it for ingestion.
// Unsampled spans are dropped unless they were promoted due to errors.
func (s *Span) End() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return
	}

	// Skip unsampled spans (unless promoted due to error)
	if !s.sampled && !s.promoted {
		return
	}

	duration := time.Since(s.StartTime)
	s.DurationNS = uint64(duration.Nanoseconds())

	// Inject SDK metadata (OpenTelemetry Semantic Conventions)
	if s.Attributes == nil {
		s.Attributes = make(map[string]string)
	}
	s.Attributes["telemetry.sdk.name"] = SDKName
	s.Attributes["telemetry.sdk.version"] = SDKVersion
	s.Attributes["telemetry.sdk.language"] = SDKLanguage

	// Mark if this span was promoted from unsampled
	if s.promoted && !s.sampled {
		s.Attributes["imprint.sampling.promoted"] = "true"
	}

	// Queue the span for sending
	select {
	case s.client.spanChan <- s:
	default:
		// Buffer full, drop span to avoid blocking application
		// In a real production SDK, we might log this or have a drop counter
	}
}

// IsSampled returns whether this span will be sent to the backend.
func (s *Span) IsSampled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sampled || s.promoted
}

// SetAttribute adds a key-value pair to the span's attributes.
// Values are converted to strings for storage.
func (s *Span) SetAttribute(k string, v interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]string)
	}
	// Convert value to string
	switch val := v.(type) {
	case string:
		s.Attributes[k] = val
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		s.Attributes[k] = fmt.Sprintf("%d", val)
	case float32, float64:
		s.Attributes[k] = fmt.Sprintf("%v", val)
	case bool:
		s.Attributes[k] = fmt.Sprintf("%t", val)
	default:
		s.Attributes[k] = fmt.Sprintf("%v", val)
	}
}

// RecordError records an error on the span.
// If the span was not sampled, it will be promoted (tail-based sampling)
// to ensure errors are always captured.
func (s *Span) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorData = err.Error()

	// Tail-based sampling: promote unsampled spans that have errors
	// This ensures we never miss error data
	if !s.sampled {
		s.promoted = true
	}
}

// SetStatus sets the HTTP status code for the span.
func (s *Span) SetStatus(code uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StatusCode = code
}

// TraceParentString returns the W3C Trace Context traceparent string for this span.
// Format: 00-{trace_id}-{span_id}-01
// This is useful for:
// - Injecting into HTML templates for RUM integration: <meta name="traceparent" content="{{ .Span.TraceParentString }}">
// - Manual context propagation to downstream services
// - Logging correlation
func (s *Span) TraceParentString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	// W3C Trace Context format: version-traceid-spanid-flags
	// version: 00 (current version)
	// flags: 01 (sampled)
	return fmt.Sprintf("00-%s-%s-01", s.TraceID, s.SpanID)
}
