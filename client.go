package imprint

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Config holds the configuration for the Imprint client.
type Config struct {
	APIKey      string
	ServiceName string
	IngestURL   string // e.g., "http://localhost:8080/v1/traces"

	// Filter Rules (optional)
	IgnorePaths      []string // Exact path matches to ignore (e.g., "/health", "/metrics")
	IgnorePrefixes   []string // Path prefixes to ignore (e.g., "/static/", "/assets/")
	IgnoreExtensions []string // File extensions to ignore (e.g., ".css", ".js"). Defaults to common static files if nil.
}

// Client is the main entry point for the SDK.
type Client struct {
	config     Config
	spanChan   chan *Span
	httpClient *http.Client
	stopChan   chan struct{}
	wg         sync.WaitGroup
}

// NewClient creates and starts a new Imprint client.
func NewClient(cfg Config) *Client {
	if cfg.IngestURL == "" {
		cfg.IngestURL = "http://localhost:8080/v1/traces"
	}

	c := &Client{
		config:     cfg,
		spanChan:   make(chan *Span, 1000), // Buffer up to 1000 spans
		httpClient: &http.Client{Timeout: 5 * time.Second},
		stopChan:   make(chan struct{}),
	}

	c.wg.Add(1)
	go c.worker()

	return c
}

// SpanOptions holds optional parameters for StartSpan.
type SpanOptions struct {
	Kind string
}

// StartSpan creates a new span.
func (c *Client) StartSpan(ctx context.Context, name string, opts ...SpanOptions) (context.Context, *Span) {
	var parentID *string
	var traceID string

	// Check for parent span in context
	if parent := FromContext(ctx); parent != nil {
		traceID = parent.TraceID
		pid := parent.SpanID
		parentID = &pid
	} else {
		// Start a new trace
		traceID = generateTraceID()
	}

	kind := "internal"
	if len(opts) > 0 && opts[0].Kind != "" {
		kind = opts[0].Kind
	}

	span := &Span{
		TraceID:   traceID,
		SpanID:    generateSpanID(),
		ParentID:  parentID,
		Namespace: c.config.ServiceName,
		Name:      name,
		Kind:      kind,
		StartTime: time.Now(),
		client:    c,
	}

	return NewContext(ctx, span), span
}

// RecordEvent creates an instant span (0ns duration) for logging events, metrics, or markers.
// Events are recorded immediately and appear in the trace waterfall as dots rather than bars.
// Use this for validation errors, business events, log entries, etc.
func (c *Client) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	var parentID *string
	var traceID string

	// Check for parent span in context
	if parent := FromContext(ctx); parent != nil {
		traceID = parent.TraceID
		pid := parent.SpanID
		parentID = &pid
	} else {
		// Start a new trace if no parent
		traceID = generateTraceID()
	}

	span := &Span{
		TraceID:    traceID,
		SpanID:     generateSpanID(),
		ParentID:   parentID,
		Namespace:  c.config.ServiceName,
		Name:       name,
		Kind:       "event",
		StartTime:  time.Now(),
		DurationNS: 0, // Events have 0 duration
		client:     c,
	}

	// Set attributes (convert to string values)
	if attributes != nil {
		span.Attributes = make(map[string]string, len(attributes))
		for k, v := range attributes {
			span.Attributes[k] = fmt.Sprintf("%v", v)
		}
	}

	// Dispatch immediately
	select {
	case c.spanChan <- span:
	default:
		// Buffer full, drop event
	}
}

// Shutdown flushes remaining spans and stops the worker.
func (c *Client) Shutdown(ctx context.Context) error {
	close(c.stopChan)

	// Wait for worker to finish with a timeout
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) worker() {
	defer c.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var buffer []*Span

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		c.sendBatch(buffer)
		buffer = nil // Clear buffer (or reallocate)
		buffer = make([]*Span, 0, 100)
	}

	for {
		select {
		case span := <-c.spanChan:
			buffer = append(buffer, span)
			if len(buffer) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-c.stopChan:
			// Drain channel
			for {
				select {
				case span := <-c.spanChan:
					buffer = append(buffer, span)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (c *Client) sendBatch(spans []*Span) {
	payload, err := json.Marshal(spans)
	if err != nil {
		// fmt.Printf("Error marshaling spans: %v\n", err)
		return
	}

	req, err := http.NewRequest("POST", c.config.IngestURL, bytes.NewReader(payload))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// fmt.Printf("Error sending spans: %v\n", err)
		return
	}
	defer resp.Body.Close()

	// if resp.StatusCode >= 400 {
	// 	fmt.Printf("Ingest API returned error: %d\n", resp.StatusCode)
	// }
}

// generateTraceID generates a 16-byte hex string (32 chars)
func generateTraceID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// generateSpanID generates a 8-byte hex string (16 chars)
func generateSpanID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
