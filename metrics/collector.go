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
	attrs := make(map[string]interface{})

	// Collect goroutine count
	if c.config.CollectGoroutines {
		goroutines := runtime.NumGoroutine()
		attrs["process.runtime.go.goroutines"] = goroutines
	}

	// Collect CPU info
	if c.config.CollectCPU {
		attrs["process.runtime.go.num_cpu"] = runtime.NumCPU()
		attrs["process.runtime.go.gomaxprocs"] = runtime.GOMAXPROCS(0)
	}

	// Collect memory and GC stats
	if c.config.CollectMemory || c.config.CollectGC {
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)

		if c.config.CollectMemory {
			// Heap memory
			attrs["process.runtime.go.mem.heap_alloc"] = memStats.HeapAlloc
			attrs["process.runtime.go.mem.heap_sys"] = memStats.HeapSys
			attrs["process.runtime.go.mem.heap_idle"] = memStats.HeapIdle
			attrs["process.runtime.go.mem.heap_inuse"] = memStats.HeapInuse
			attrs["process.runtime.go.mem.heap_released"] = memStats.HeapReleased
			attrs["process.runtime.go.mem.heap_objects"] = memStats.HeapObjects

			// Stack memory
			attrs["process.runtime.go.mem.stack_inuse"] = memStats.StackInuse
			attrs["process.runtime.go.mem.stack_sys"] = memStats.StackSys

			// Total allocations
			attrs["process.runtime.go.mem.alloc"] = memStats.Alloc
			attrs["process.runtime.go.mem.total_alloc"] = memStats.TotalAlloc
			attrs["process.runtime.go.mem.sys"] = memStats.Sys
			attrs["process.runtime.go.mem.mallocs"] = memStats.Mallocs
			attrs["process.runtime.go.mem.frees"] = memStats.Frees
		}

		if c.config.CollectGC {
			attrs["process.runtime.go.gc.count"] = memStats.NumGC
			attrs["process.runtime.go.gc.pause_total_ns"] = memStats.PauseTotalNs
			attrs["process.runtime.go.gc.num_forced"] = memStats.NumForcedGC

			// Last GC pause time (if any GCs have occurred)
			if memStats.NumGC > 0 {
				// Get the most recent pause time from the circular buffer
				lastPauseIdx := (memStats.NumGC + 255) % 256
				attrs["process.runtime.go.gc.last_pause_ns"] = memStats.PauseNs[lastPauseIdx]
			}
		}
	}

	// Emit as a gauge event (0ms duration span)
	c.client.RecordEvent(ctx, "runtime.metrics", attrs)
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
