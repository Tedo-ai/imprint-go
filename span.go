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
	DurationNS uint64    `json:"duration_ns,string"`
	StatusCode uint16    `json:"status_code"`
	ErrorData  string            `json:"error_data,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`

	mu     sync.Mutex
	client *Client
}

// End marks the span as completed and queues it for ingestion.
func (s *Span) End() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return
	}

	duration := time.Since(s.StartTime)
	s.DurationNS = uint64(duration.Nanoseconds())

	// Queue the span for sending
	select {
	case s.client.spanChan <- s:
	default:
		// Buffer full, drop span to avoid blocking application
		// In a real production SDK, we might log this or have a drop counter
	}
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
func (s *Span) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorData = err.Error()
	// Optionally set status code to 500 if not already set?
	// For now, we leave status code control to the user or middleware.
}

// SetStatus sets the HTTP status code for the span.
func (s *Span) SetStatus(code uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StatusCode = code
}
