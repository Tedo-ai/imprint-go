package imprint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// MetricType represents the type of metric being recorded.
type MetricType string

const (
	// MetricTypeCounter is a monotonically increasing counter (e.g., request count).
	MetricTypeCounter MetricType = "counter"

	// MetricTypeGauge is a value that can go up or down (e.g., memory usage).
	MetricTypeGauge MetricType = "gauge"

	// MetricTypeHistogram records the distribution of values (e.g., request duration).
	MetricTypeHistogram MetricType = "histogram"
)

// Metric represents a single metric data point for transmission.
type Metric struct {
	Name             string            `json:"name"`
	Type             MetricType        `json:"type"`
	Value            float64           `json:"value"`
	Labels           map[string]string `json:"labels,omitempty"`
	Timestamp        time.Time         `json:"timestamp"`
	Unit             string            `json:"unit,omitempty"`
	HistogramBuckets []float64         `json:"histogram_buckets,omitempty"`
	HistogramCounts  []uint64          `json:"histogram_counts,omitempty"`
	Sum              float64           `json:"sum,omitempty"`
	Count            uint64            `json:"count,omitempty"`
	Min              float64           `json:"min,omitempty"`
	Max              float64           `json:"max,omitempty"`
}

// DefaultHistogramBuckets returns default bucket boundaries for latency histograms in milliseconds.
var DefaultHistogramBuckets = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// MetricsClient handles batching and sending metrics to the Imprint backend.
type MetricsClient struct {
	config         Config
	metricsChan    chan Metric
	httpClient     *http.Client
	stopChan       chan struct{}
	wg             sync.WaitGroup
	mu             sync.RWMutex
	histogramState map[string]*histogramAggregator
}

type histogramAggregator struct {
	name    string
	labels  map[string]string
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
	min     float64
	max     float64
}

// NewMetricsClient creates a new metrics client for sending metrics.
func NewMetricsClient(cfg Config) *MetricsClient {
	if cfg.IngestURL == "" {
		cfg.IngestURL = "https://ingest.imprint.cloud/v1/spans"
	}

	mc := &MetricsClient{
		config:         cfg,
		metricsChan:    make(chan Metric, 1000),
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		stopChan:       make(chan struct{}),
		histogramState: make(map[string]*histogramAggregator),
	}

	mc.wg.Add(1)
	go mc.worker()

	return mc
}

// metricsURL returns the metrics endpoint URL derived from the ingest URL.
func (mc *MetricsClient) metricsURL() string {
	// Replace /v1/spans with /v1/metrics
	url := mc.config.IngestURL
	if len(url) > 10 && url[len(url)-10:] == "/v1/spans" {
		return url[:len(url)-10] + "/v1/metrics"
	}
	return url + "/v1/metrics"
}

// Increment records a counter increment.
// Counters are monotonically increasing and reset on process restart.
func (mc *MetricsClient) Increment(ctx context.Context, name string, value float64, labels map[string]string) {
	mc.record(ctx, name, MetricTypeCounter, value, labels, "")
}

// Gauge records a gauge metric value.
// Gauges represent a snapshot of a value at a point in time.
func (mc *MetricsClient) Gauge(ctx context.Context, name string, value float64, labels map[string]string) {
	mc.record(ctx, name, MetricTypeGauge, value, labels, "")
}

// GaugeWithUnit records a gauge metric value with a unit.
func (mc *MetricsClient) GaugeWithUnit(ctx context.Context, name string, value float64, labels map[string]string, unit string) {
	mc.record(ctx, name, MetricTypeGauge, value, labels, unit)
}

// Histogram records a histogram observation.
// Use this for timing and distributions.
func (mc *MetricsClient) Histogram(ctx context.Context, name string, value float64, labels map[string]string) {
	mc.HistogramWithBuckets(ctx, name, value, labels, DefaultHistogramBuckets)
}

// HistogramWithBuckets records a histogram observation with custom bucket boundaries.
func (mc *MetricsClient) HistogramWithBuckets(ctx context.Context, name string, value float64, labels map[string]string, buckets []float64) {
	normalizedLabels := mc.normalizeLabels(labels)
	key := mc.histogramKey(name, normalizedLabels)

	mc.mu.Lock()
	agg, exists := mc.histogramState[key]
	if !exists {
		agg = &histogramAggregator{
			name:    name,
			labels:  normalizedLabels,
			buckets: buckets,
			counts:  make([]uint64, len(buckets)+1),
			min:     math.MaxFloat64,
			max:     -math.MaxFloat64,
		}
		mc.histogramState[key] = agg
	}

	// Find bucket and increment count
	bucketIdx := sort.SearchFloat64s(buckets, value)
	if bucketIdx >= len(buckets) || value > buckets[bucketIdx] {
		bucketIdx = len(buckets) // Overflow bucket
	}
	agg.counts[bucketIdx]++
	agg.sum += value
	agg.count++
	if value < agg.min {
		agg.min = value
	}
	if value > agg.max {
		agg.max = value
	}
	mc.mu.Unlock()
}

// Time measures the duration of a function and records it as a histogram.
func (mc *MetricsClient) Time(ctx context.Context, name string, labels map[string]string, fn func()) {
	start := time.Now()
	fn()
	durationMs := float64(time.Since(start).Nanoseconds()) / 1e6
	mc.Histogram(ctx, name, durationMs, labels)
}

// TimeFunc measures the duration of a function and records it as a histogram, returning the result.
func TimeFunc[T any](mc *MetricsClient, ctx context.Context, name string, labels map[string]string, fn func() T) T {
	start := time.Now()
	result := fn()
	durationMs := float64(time.Since(start).Nanoseconds()) / 1e6
	mc.Histogram(ctx, name, durationMs, labels)
	return result
}

// Shutdown flushes remaining metrics and stops the worker.
func (mc *MetricsClient) Shutdown(ctx context.Context) error {
	close(mc.stopChan)

	done := make(chan struct{})
	go func() {
		mc.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (mc *MetricsClient) record(ctx context.Context, name string, metricType MetricType, value float64, labels map[string]string, unit string) {
	metric := Metric{
		Name:      name,
		Type:      metricType,
		Value:     value,
		Labels:    mc.normalizeLabels(labels),
		Timestamp: time.Now(),
		Unit:      unit,
	}

	select {
	case mc.metricsChan <- metric:
	default:
		// Buffer full, drop metric
	}
}

func (mc *MetricsClient) normalizeLabels(labels map[string]string) map[string]string {
	normalized := make(map[string]string, len(labels)+2)
	for k, v := range labels {
		normalized[k] = v
	}

	// Add service.instance.id if not present
	if _, ok := normalized["service.instance.id"]; !ok {
		if hostname, err := os.Hostname(); err == nil {
			normalized["service.instance.id"] = hostname
		}
	}

	// Add service name if not present
	if _, ok := normalized["service"]; !ok {
		normalized["service"] = mc.config.ServiceName
	}

	return normalized
}

func (mc *MetricsClient) histogramKey(name string, labels map[string]string) string {
	// Create a deterministic key from name and sorted labels
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	key := name
	for _, k := range keys {
		key += ":" + k + "=" + labels[k]
	}
	return key
}

func (mc *MetricsClient) worker() {
	defer mc.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var buffer []Metric

	flush := func() {
		// Flush regular metrics
		if len(buffer) > 0 {
			mc.sendBatch(buffer)
			buffer = nil
		}

		// Flush histograms
		mc.flushHistograms()
	}

	for {
		select {
		case metric := <-mc.metricsChan:
			buffer = append(buffer, metric)
			if len(buffer) >= 100 {
				mc.sendBatch(buffer)
				buffer = nil
			}
		case <-ticker.C:
			flush()
		case <-mc.stopChan:
			// Drain channel
			for {
				select {
				case metric := <-mc.metricsChan:
					buffer = append(buffer, metric)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (mc *MetricsClient) flushHistograms() {
	mc.mu.Lock()
	if len(mc.histogramState) == 0 {
		mc.mu.Unlock()
		return
	}

	histograms := make([]Metric, 0, len(mc.histogramState))
	for _, agg := range mc.histogramState {
		minVal := agg.min
		maxVal := agg.max
		if minVal == math.MaxFloat64 {
			minVal = 0
		}
		if maxVal == -math.MaxFloat64 {
			maxVal = 0
		}

		histograms = append(histograms, Metric{
			Name:             agg.name,
			Type:             MetricTypeHistogram,
			Value:            0,
			Labels:           agg.labels,
			Timestamp:        time.Now(),
			HistogramBuckets: agg.buckets,
			HistogramCounts:  agg.counts,
			Sum:              agg.sum,
			Count:            agg.count,
			Min:              minVal,
			Max:              maxVal,
		})
	}
	mc.histogramState = make(map[string]*histogramAggregator)
	mc.mu.Unlock()

	if len(histograms) > 0 {
		mc.sendBatch(histograms)
	}
}

func (mc *MetricsClient) sendBatch(metrics []Metric) {
	payload, err := json.Marshal(metrics)
	if err != nil {
		fmt.Printf("Error marshaling metrics: %v\n", err)
		return
	}

	req, err := http.NewRequest("POST", mc.metricsURL(), bytes.NewReader(payload))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mc.config.APIKey)

	resp, err := mc.httpClient.Do(req)
	if err != nil {
		fmt.Printf("Error sending metrics: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fmt.Printf("Metrics API returned error: %d\n", resp.StatusCode)
	}
}

// ============================================================================
// Convenience methods on Client for backward compatibility
// ============================================================================

// RecordCounter records a counter metric increment.
// Alias for metricsClient.Increment for simpler API.
func (c *Client) RecordCounter(ctx context.Context, name string, value float64, labels map[string]string) {
	if c.metricsClient == nil {
		c.initMetricsClient()
	}
	c.metricsClient.Increment(ctx, name, value, labels)
}

// RecordHistogram records a histogram observation.
func (c *Client) RecordHistogram(ctx context.Context, name string, value float64, labels map[string]string) {
	if c.metricsClient == nil {
		c.initMetricsClient()
	}
	c.metricsClient.Histogram(ctx, name, value, labels)
}

// RecordTiming is a convenience method to record a duration in milliseconds as a histogram.
func (c *Client) RecordTiming(ctx context.Context, name string, durationMs float64, labels map[string]string) {
	if c.metricsClient == nil {
		c.initMetricsClient()
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["unit"] = "ms"
	c.metricsClient.Histogram(ctx, name, durationMs, labels)
}

// Time measures the duration of a function and records it as a histogram.
func (c *Client) Time(ctx context.Context, name string, labels map[string]string, fn func()) {
	if c.metricsClient == nil {
		c.initMetricsClient()
	}
	c.metricsClient.Time(ctx, name, labels, fn)
}

// metricsClient is lazily initialized when metrics methods are called.
var metricsClientOnce sync.Once

func (c *Client) initMetricsClient() {
	metricsClientOnce.Do(func() {
		c.metricsClient = NewMetricsClient(c.config)
	})
}
