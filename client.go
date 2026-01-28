package imprint

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Config holds the configuration for the Imprint client.
type Config struct {
	APIKey      string
	ServiceName string
	IngestURL   string // e.g., "https://ingest.imprint.cloud/v1/spans"

	// Filter Rules (optional)
	IgnorePaths      []string // Exact path matches to ignore (e.g., "/health", "/metrics")
	IgnorePrefixes   []string // Path prefixes to ignore (e.g., "/static/", "/assets/")
	IgnoreExtensions []string // File extensions to ignore (e.g., ".css", ".js"). Defaults to common static files if nil.

	// Sampling (optional)
	// SamplingRate controls the percentage of traces to sample (0.0 to 1.0).
	// Default: 1.0 (sample all). Set to 0.5 to sample ~50% of traces.
	// Sampling is trace-ID consistent, meaning the same trace ID always gets
	// the same sampling decision across services.
	// Note: Spans with errors are always sent (tail-based promotion).
	SamplingRate float64

	// Metrics (optional)
	// EnableMetrics starts a background collector for runtime metrics.
	EnableMetrics bool
	// MetricsInterval is how often to collect metrics. Default: 60s.
	MetricsInterval time.Duration
}

// Sampler determines whether a trace should be sampled.
type Sampler interface {
	ShouldSample(traceID string) bool
}

// Client is the main entry point for the SDK.
type Client struct {
	config        Config
	spanChan      chan *Span
	logChan       chan LogEntry
	httpClient    *http.Client
	stopChan      chan struct{}
	wg            sync.WaitGroup
	sampler       Sampler
	stopMetrics   func()         // stops the metrics collector if enabled
	metricsClient *MetricsClient // lazily initialized when metrics methods are used
}

// NewClient creates and starts a new Imprint client.
func NewClient(cfg Config) *Client {
	if cfg.IngestURL == "" {
		cfg.IngestURL = "https://ingest.imprint.cloud/v1/spans"
	}

	// Default sampling rate to 1.0 (sample all) if not set
	if cfg.SamplingRate == 0 {
		cfg.SamplingRate = 1.0
	}

	c := &Client{
		config:     cfg,
		spanChan:   make(chan *Span, 1000),     // Buffer up to 1000 spans
		logChan:    make(chan LogEntry, 1000), // Buffer up to 1000 logs
		httpClient: &http.Client{Timeout: 5 * time.Second},
		stopChan:   make(chan struct{}),
		sampler:    newRateSampler(cfg.SamplingRate),
	}

	c.wg.Add(2) // One for spans worker, one for logs worker
	go c.worker()
	go c.logWorker()

	return c
}

// SetSampler allows setting a custom sampler after client creation.
func (c *Client) SetSampler(s Sampler) {
	c.sampler = s
}

// rateSampler is a simple internal sampler for head-based sampling.
type rateSampler struct {
	rate      float64
	threshold uint64
}

func newRateSampler(rate float64) *rateSampler {
	if rate <= 0 {
		return &rateSampler{rate: 0, threshold: 0}
	}
	if rate >= 1 {
		return &rateSampler{rate: 1, threshold: ^uint64(0)}
	}
	threshold := uint64(float64(^uint64(0)) * rate)
	return &rateSampler{rate: rate, threshold: threshold}
}

func (s *rateSampler) ShouldSample(traceID string) bool {
	if s.rate >= 1 {
		return true
	}
	if s.rate <= 0 {
		return false
	}
	hash := hashTraceID(traceID)
	return hash < s.threshold
}

// hashTraceID creates a consistent hash from a trace ID using FNV-1a.
func hashTraceID(traceID string) uint64 {
	// Simple FNV-1a hash
	var hash uint64 = 14695981039346656037 // FNV offset basis
	for i := 0; i < len(traceID); i++ {
		hash ^= uint64(traceID[i])
		hash *= 1099511628211 // FNV prime
	}
	return hash
}

// SpanOptions holds optional parameters for StartSpan.
type SpanOptions struct {
	Kind string
}

// StartSpan creates a new span.
func (c *Client) StartSpan(ctx context.Context, name string, opts ...SpanOptions) (context.Context, *Span) {
	var parentID *string
	var traceID string
	var sampled bool
	isRoot := false

	// Check for parent span in context
	if parent := FromContext(ctx); parent != nil {
		traceID = parent.TraceID
		pid := parent.SpanID
		parentID = &pid
		// Inherit sampling decision from parent
		sampled = parent.sampled
	} else {
		// Start a new trace
		traceID = generateTraceID()
		isRoot = true
		// Make sampling decision for root spans
		sampled = c.sampler.ShouldSample(traceID)
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
		sampled:   sampled,
		isRoot:    isRoot,
	}

	return NewContext(ctx, span), span
}

// StartDBSpan is a convenience method for creating database client spans.
// It automatically sets the span kind to "client" and adds standard db.* attributes.
// Use this for manual database instrumentation when auto-instrumentation isn't available.
//
// Usage:
//
//	ctx, span := client.StartDBSpan(ctx, "SELECT users", "postgresql", "SELECT * FROM users WHERE id = $1")
//	defer span.End()
//	// ... execute query ...
func (c *Client) StartDBSpan(ctx context.Context, name, dbSystem, statement string) (context.Context, *Span) {
	ctx, span := c.StartSpan(ctx, name, SpanOptions{Kind: "client"})
	span.SetAttribute("db.system", dbSystem)
	if statement != "" {
		span.SetAttribute("db.statement", statement)
	}
	return ctx, span
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
		span.Attributes = make(map[string]string, len(attributes)+3)
		for k, v := range attributes {
			span.Attributes[k] = fmt.Sprintf("%v", v)
		}
	} else {
		span.Attributes = make(map[string]string, 3)
	}

	// Inject SDK metadata (OpenTelemetry Semantic Conventions)
	span.Attributes["telemetry.sdk.name"] = SDKName
	span.Attributes["telemetry.sdk.version"] = SDKVersion
	span.Attributes["telemetry.sdk.language"] = SDKLanguage

	// Dispatch immediately
	select {
	case c.spanChan <- span:
	default:
		// Buffer full, drop event
	}
}

// RecordGauge records a gauge metric value (numeric measurement at a point in time).
// Gauges are used for values that can go up or down, such as:
// - Memory usage (process.runtime.go.mem.heap_alloc)
// - CPU percentage
// - Queue depth
// - Active connections
//
// The value is stored in the "metric.value" attribute, which the dashboard
// uses to distinguish gauges from counters and render them as line charts.
//
// A "service.instance.id" attribute is automatically added using the hostname
// if not already present, enabling multi-instance aggregation in the dashboard.
func (c *Client) RecordGauge(ctx context.Context, name string, value float64, attributes map[string]interface{}) {
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
		DurationNS: 0, // Gauges have 0 duration (instant measurement)
		client:     c,
	}

	// Set attributes (convert to string values)
	if attributes != nil {
		span.Attributes = make(map[string]string, len(attributes)+5)
		for k, v := range attributes {
			span.Attributes[k] = fmt.Sprintf("%v", v)
		}
	} else {
		span.Attributes = make(map[string]string, 5)
	}

	// Set the gauge value - this is what makes it a gauge vs counter
	span.Attributes["metric.value"] = fmt.Sprintf("%v", value)

	// Auto-inject service.instance.id (hostname) if not present
	// This enables multi-instance aggregation in the dashboard
	if _, ok := span.Attributes["service.instance.id"]; !ok {
		if hostname, err := os.Hostname(); err == nil {
			span.Attributes["service.instance.id"] = hostname
		}
	}

	// Inject SDK metadata (OpenTelemetry Semantic Conventions)
	span.Attributes["telemetry.sdk.name"] = SDKName
	span.Attributes["telemetry.sdk.version"] = SDKVersion
	span.Attributes["telemetry.sdk.language"] = SDKLanguage

	// Dispatch immediately
	select {
	case c.spanChan <- span:
	default:
		// Buffer full, drop gauge
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
		fmt.Printf("Error marshaling spans: %v\n", err)
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
		fmt.Printf("Error sending spans: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fmt.Printf("Ingest API returned error: %d\n", resp.StatusCode)
	} else {
		fmt.Printf("Successfully sent batch of %d spans\n", len(spans))
	}
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
