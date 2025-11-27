// Package metrics provides automatic Go runtime metrics collection for Imprint.
//
// The collector periodically samples runtime statistics (memory, goroutines, GC)
// and emits them as gauge spans that can be visualized in the Imprint dashboard.
//
// Usage:
//
//	import (
//	    imprint "github.com/tedo-ai/imprint-go"
//	    "github.com/tedo-ai/imprint-go/metrics"
//	)
//
//	client := imprint.NewClient(imprint.Config{...})
//	collector := metrics.NewCollector(client, metrics.DefaultConfig())
//	collector.Start()
//	defer collector.Stop()
//
// Or use the simpler integration via client config:
//
//	client := imprint.NewClient(imprint.Config{
//	    EnableMetrics:   true,
//	    MetricsInterval: 60 * time.Second,
//	})
package metrics

import (
	"context"
	"runtime"
	"sync"
	"time"

	imprint "github.com/tedo-ai/imprint-go"
)

// Config holds configuration for the metrics collector.
type Config struct {
	// Interval between metric collections. Default: 60 seconds.
	Interval time.Duration

	// Namespace for metric names. Default: "runtime".
	Namespace string

	// CollectMemory enables memory statistics. Default: true.
	CollectMemory bool

	// CollectGoroutines enables goroutine count. Default: true.
	CollectGoroutines bool

	// CollectGC enables garbage collection statistics. Default: true.
	CollectGC bool

	// CollectCPU enables CPU statistics (NumCPU, GOMAXPROCS). Default: true.
	CollectCPU bool
}

// DefaultConfig returns sensible defaults for the metrics collector.
func DefaultConfig() Config {
	return Config{
		Interval:          60 * time.Second,
		Namespace:         "runtime",
		CollectMemory:     true,
		CollectGoroutines: true,
		CollectGC:         true,
		CollectCPU:        true,
	}
}

// Collector periodically collects and reports Go runtime metrics.
type Collector struct {
	client   *imprint.Client
	config   Config
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

// NewCollector creates a new metrics collector.
func NewCollector(client *imprint.Client, config Config) *Collector {
	if config.Interval <= 0 {
		config.Interval = 60 * time.Second
	}
	if config.Namespace == "" {
		config.Namespace = "runtime"
	}
	return &Collector{
		client:   client,
		config:   config,
		stopChan: make(chan struct{}),
	}
}

// Start begins collecting metrics at the configured interval.
// It's safe to call Start multiple times; subsequent calls are no-ops.
func (c *Collector) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return
	}

	c.running = true
	c.stopChan = make(chan struct{})
	c.wg.Add(1)
	go c.loop()
}

// Stop stops the metrics collector and waits for it to finish.
func (c *Collector) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	close(c.stopChan)
	c.mu.Unlock()

	c.wg.Wait()
}

// CollectOnce collects metrics immediately without waiting for the interval.
// Useful for testing or one-off collection.
func (c *Collector) CollectOnce() {
	c.collect()
}

func (c *Collector) loop() {
	defer c.wg.Done()

	// Collect immediately on start
	c.collect()

	ticker := time.NewTicker(c.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.collect()
		case <-c.stopChan:
			return
		}
	}
}

func (c *Collector) collect() {
	ctx := context.Background()

	// Collect goroutine count
	if c.config.CollectGoroutines {
		c.client.RecordGauge(ctx, "process.runtime.go.goroutines", float64(runtime.NumGoroutine()), nil)
	}

	// Collect CPU info
	if c.config.CollectCPU {
		c.client.RecordGauge(ctx, "process.runtime.go.num_cpu", float64(runtime.NumCPU()), nil)
		c.client.RecordGauge(ctx, "process.runtime.go.gomaxprocs", float64(runtime.GOMAXPROCS(0)), nil)
	}

	// Collect memory and GC stats
	if c.config.CollectMemory || c.config.CollectGC {
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)

		if c.config.CollectMemory {
			// Heap memory (in bytes, converted to MB for readability in dashboard)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.heap_alloc", float64(memStats.HeapAlloc), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.heap_sys", float64(memStats.HeapSys), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.heap_idle", float64(memStats.HeapIdle), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.heap_inuse", float64(memStats.HeapInuse), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.heap_released", float64(memStats.HeapReleased), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.heap_objects", float64(memStats.HeapObjects), nil)

			// Stack memory
			c.client.RecordGauge(ctx, "process.runtime.go.mem.stack_inuse", float64(memStats.StackInuse), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.stack_sys", float64(memStats.StackSys), nil)

			// Total allocations
			c.client.RecordGauge(ctx, "process.runtime.go.mem.alloc", float64(memStats.Alloc), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.total_alloc", float64(memStats.TotalAlloc), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.sys", float64(memStats.Sys), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.mallocs", float64(memStats.Mallocs), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.mem.frees", float64(memStats.Frees), nil)
		}

		if c.config.CollectGC {
			c.client.RecordGauge(ctx, "process.runtime.go.gc.count", float64(memStats.NumGC), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.gc.pause_total_ns", float64(memStats.PauseTotalNs), nil)
			c.client.RecordGauge(ctx, "process.runtime.go.gc.num_forced", float64(memStats.NumForcedGC), nil)

			// Last GC pause time (if any GCs have occurred)
			if memStats.NumGC > 0 {
				// Get the most recent pause time from the circular buffer
				lastPauseIdx := (memStats.NumGC + 255) % 256
				c.client.RecordGauge(ctx, "process.runtime.go.gc.last_pause_ns", float64(memStats.PauseNs[lastPauseIdx]), nil)
			}
		}
	}
}

// StartWithClient is a convenience function that creates and starts a collector
// with default configuration, returning a stop function.
func StartWithClient(client *imprint.Client) func() {
	collector := NewCollector(client, DefaultConfig())
	collector.Start()
	return collector.Stop
}

// StartWithConfig is like StartWithClient but with custom configuration.
func StartWithConfig(client *imprint.Client, config Config) func() {
	collector := NewCollector(client, config)
	collector.Start()
	return collector.Stop
}
