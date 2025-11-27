# Imprint Go SDK

The official Go client for [Imprint](https://github.com/tedo-ai/imprint). This SDK provides comprehensive observability instrumentation for Go applications including HTTP tracing, database queries, Redis, logging, and runtime metrics.

## Installation

```bash
go get github.com/tedo-ai/imprint-go
```

## Quick Start

```go
package main

import (
    "context"
    "net/http"
    imprint "github.com/tedo-ai/imprint-go"
)

func main() {
    client := imprint.NewClient(imprint.Config{
        APIKey:       "imp_live_xxxxxxxxxxxx",
        ServiceName:  "my-service",
        IngestURL:    "https://api.imprint.cloud/v1/spans",
        SamplingRate: 1.0, // Sample all traces (default)
    })
    defer client.Shutdown(context.Background())

    mux := http.NewServeMux()
    mux.HandleFunc("/hello", helloHandler)

    // Wrap with tracing middleware
    http.ListenAndServe(":8000", client.Middleware(mux))
}
```

## Table of Contents

- [Configuration](#configuration)
- [HTTP Middleware](#http-middleware)
- [Manual Instrumentation](#manual-instrumentation)
- [Gauge Metrics](#gauge-metrics)
- [Outbound HTTP Tracing](#outbound-http-tracing)
- [SQL Instrumentation](#sql-instrumentation)
- [Redis Instrumentation](#redis-instrumentation)
- [Log Correlation (slog)](#log-correlation-slog)
- [Context Propagation](#context-propagation)
- [Tracing Suppression](#tracing-suppression)
- [Panic Recovery](#panic-recovery)
- [Runtime Metrics](#runtime-metrics)
- [WebSocket Instrumentation](#websocket-instrumentation)
- [Sampling](#sampling)
- [RUM Integration](#rum-integration)
- [SDK Metadata](#sdk-metadata)

## Configuration

```go
client := imprint.NewClient(imprint.Config{
    // Required
    APIKey:      "imp_live_xxxxxxxxxxxx",
    ServiceName: "my-service",

    // Optional
    IngestURL:    "https://api.imprint.cloud/v1/spans", // Production
    SamplingRate: 0.5,  // Sample 50% of traces (errors always captured)

    // Request filtering
    IgnorePaths:      []string{"/health", "/metrics"},
    IgnorePrefixes:   []string{"/static/", "/assets/"},
    IgnoreExtensions: []string{".css", ".js", ".png"}, // Has sensible defaults
})
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `APIKey` | `string` | - | **Required**. Your project's API key. |
| `ServiceName` | `string` | - | **Required**. Identifier for this service. |
| `IngestURL` | `string` | `https://api.imprint.cloud/v1/spans` | Imprint Ingest API URL. |
| `SamplingRate` | `float64` | `1.0` | Percentage of traces to sample (0.0-1.0). |
| `IgnorePaths` | `[]string` | `nil` | Exact paths to ignore. |
| `IgnorePrefixes` | `[]string` | `nil` | Path prefixes to ignore. |
| `IgnoreExtensions` | `[]string` | see below | File extensions to ignore. |
| `EnableMetrics` | `bool` | `false` | Start background runtime metrics collector. |
| `MetricsInterval` | `time.Duration` | `60s` | Interval for metrics collection. |

**Default Ignored Extensions:**

If `IgnoreExtensions` is not set, these extensions are ignored by default:
```
.css, .js, .png, .jpg, .jpeg, .gif, .ico, .svg, .woff, .woff2, .ttf, .eot, .map
```

## HTTP Middleware

The middleware automatically traces incoming HTTP requests:

```go
mux := http.NewServeMux()
mux.HandleFunc("/api/users", handleUsers)

// Basic middleware
handler := client.Middleware(mux)

// With panic recovery (recommended)
handler = imprint.RecoveryMiddleware(client.Middleware(mux))

http.ListenAndServe(":8000", handler)
```

Features:
- Creates server spans for each request
- Captures HTTP method, path, status code
- Propagates W3C `traceparent` headers
- Respects ignore rules
- Supports WebSocket upgrades (`http.Hijacker`)
- Supports SSE and streaming responses (`http.Flusher`)

## Manual Instrumentation

### Creating Spans

```go
func processOrder(ctx context.Context, orderID string) error {
    ctx, span := client.StartSpan(ctx, "process_order")
    defer span.End()

    span.SetAttribute("order_id", orderID)
    span.SetAttribute("priority", "high")

    // Nested span
    ctx, dbSpan := client.StartSpan(ctx, "fetch_order", imprint.SpanOptions{
        Kind: "client",
    })
    defer dbSpan.End()

    // ... do work
    return nil
}
```

### Recording Events

Events are instant spans (0ms duration) useful for logging and business events:

```go
client.RecordEvent(ctx, "order_created", map[string]interface{}{
    "order_id": orderID,
    "amount":   99.99,
    "currency": "USD",
})
```

### Recording Gauge Metrics

Gauges are numeric values that can go up or down over time (memory usage, queue depth, active connections). They appear as **line charts** in the dashboard:

```go
// Record a gauge metric
client.RecordGauge(ctx, "queue.depth", float64(len(queue)), map[string]interface{}{
    "queue_name": "orders",
})

// Memory usage example
client.RecordGauge(ctx, "cache.size_bytes", float64(cache.Size()), nil)

// Active connections
client.RecordGauge(ctx, "db.connections.active", float64(pool.ActiveCount()), nil)
```

**Key behaviors:**
- Sets `metric.value` attribute automatically (this is what makes it a gauge vs counter)
- Auto-injects `service.instance.id` (hostname) for **multi-instance aggregation** in the dashboard
- Dashboard shows multiple lines when the same metric is emitted from different instances

### Recording Errors

```go
if err != nil {
    span.RecordError(err) // Also promotes unsampled spans
    span.SetStatus(500)
}
```

### Accessing Current Span

Get the current span from context (e.g., in nested functions):

```go
func innerFunction(ctx context.Context) {
    // Get current span without creating a new one
    span := imprint.FromContext(ctx)
    if span != nil {
        span.SetAttribute("inner_value", "something")
    }
}
```

## Outbound HTTP Tracing

Trace calls to external APIs (OpenAI, payment gateways, etc.):

```go
import imprint "github.com/tedo-ai/imprint-go"

// Wrap an existing client
httpClient := imprint.WrapClient(&http.Client{}, imprintClient)

// Or create a new traced client
httpClient := imprint.NewTracedClient(imprintClient)

// Use normally - traces are automatic
resp, err := httpClient.Get("https://api.openai.com/v1/chat/completions")
```

Each outbound request creates a span with:
- `kind: client`
- `http.method`, `http.url`, `http.status_code`
- Automatic `traceparent` header injection

### Using Transport Directly

For more control, use the `Transport` struct directly:

```go
client := &http.Client{
    Transport: &imprint.Transport{
        Base:   customTransport,  // Your existing transport (optional)
        Client: imprintClient,
    },
    Timeout: 30 * time.Second,
}
```

## SQL Instrumentation

```go
import imprintsql "github.com/tedo-ai/imprint-go/sql"

// Wrap your database connection
db, err := imprintsql.Open("postgres", connectionString,
    imprintsql.WithClient(imprintClient),
    imprintsql.WithDBSystem("postgresql"),
)

// Use normally - queries are traced automatically
rows, err := db.QueryContext(ctx, "SELECT * FROM users WHERE id = $1", userID)
```

### SQL Options

```go
db, err := imprintsql.Open("postgres", connectionString,
    imprintsql.WithClient(imprintClient),
    imprintsql.WithDBSystem("postgresql"),   // Database system name for attributes
    imprintsql.WithRecordStatement(true),    // Record SQL in spans (default: true)
)
```

### Span Naming

SQL spans are automatically named based on the query operation:
- `SELECT users` - for `SELECT * FROM users ...`
- `INSERT orders` - for `INSERT INTO orders ...`
- `UPDATE products` - for `UPDATE products SET ...`
- `DELETE sessions` - for `DELETE FROM sessions ...`

Span attributes:
- `db.system`: Database type (postgresql, mysql, sqlite, etc.)
- `db.statement`: Full SQL query (if recording enabled)

## Redis Instrumentation

```go
import (
    "github.com/redis/go-redis/v9"
    imprintredis "github.com/tedo-ai/imprint-go/redis"
)

rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
rdb.AddHook(imprintredis.NewHook(imprintClient))

// Or use the convenience wrapper
rdb := imprintredis.WrapClient(redis.NewClient(&redis.Options{...}), imprintClient)

// All commands are traced
rdb.Set(ctx, "user:123", "data", time.Hour)
rdb.Get(ctx, "user:123")
```

Span attributes:
- `db.system: redis`
- `db.operation: GET/SET/etc.`
- `db.statement: GET user:123` (key only, values omitted for privacy)

### Redis Hook Options

```go
hook := imprintredis.NewHookWithOptions(imprintClient, imprintredis.HookOptions{
    DBSystem:    "redis",        // Database system name (default: "redis")
    IncludeArgs: true,           // Include command key in traces (default: true)
})
rdb.AddHook(hook)
```

### Cluster Client

```go
clusterClient := redis.NewClusterClient(&redis.ClusterOptions{...})
imprintredis.WrapClusterClient(clusterClient, imprintClient)
```

## Log Correlation (slog)

Automatically inject trace IDs into structured logs:

```go
import (
    "log/slog"
    imprintslog "github.com/tedo-ai/imprint-go/slog"
)

// Create trace-aware handler
handler := imprintslog.NewJSONHandler(os.Stdout, nil)
logger := slog.New(handler)

// In your handlers (ctx contains trace from middleware)
logger.InfoContext(ctx, "Processing order",
    "order_id", orderID,
    "user_id", userID,
)
// Output: {"level":"INFO","msg":"Processing order","order_id":"123","user_id":"456","trace_id":"abc...","span_id":"def..."}
```

Options:

```go
handler := imprintslog.NewHandlerWithOptions(parentHandler, imprintslog.HandlerOptions{
    TraceIDKey:     "trace_id",      // Default
    SpanIDKey:      "span_id",       // Default
    AddTraceParent: true,            // Include full traceparent string
    TraceParentKey: "traceparent",   // Key for traceparent string
})
```

### Pre-bound Logger

Create a logger already bound to a trace context:

```go
// Get a logger with trace IDs pre-attached
tracedLogger := imprintslog.LoggerFromContext(ctx, logger)
tracedLogger.Info("This log has trace context even without InfoContext")
```

### Handler Types

```go
// JSON output (structured logs)
handler := imprintslog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelDebug,
})

// Text output (human-readable)
handler := imprintslog.NewTextHandler(os.Stdout, nil)

// Wrap any existing handler
handler := imprintslog.NewHandler(yourCustomHandler)
```

## Context Propagation

For async job queues, event buses, or custom RPC systems:

### Serializing Context (Producer)

```go
func enqueueJob(ctx context.Context, job Job) {
    // Inject trace context into carrier
    carrier := make(map[string]string)
    imprint.Inject(ctx, carrier)

    job.Metadata = carrier
    queue.Push(job)
}
```

### Deserializing Context (Consumer)

```go
func processJob(job Job) {
    // Extract trace context
    ctx := imprint.Extract(job.Metadata)

    // Child spans are linked to original trace
    ctx, span := client.StartSpan(ctx, "process_job")
    defer span.End()
}
```

### HTTP Header Style

For systems using `map[string][]string` (like `http.Header`):

```go
// Inject into headers
headers := make(map[string][]string)
imprint.InjectToHeaders(ctx, headers)

// Extract from headers
ctx := imprint.ExtractFromHeaders(headers)

// With existing context
ctx := imprint.ExtractFromHeadersWithContext(existingCtx, headers)
```

## Tracing Suppression

Temporarily disable tracing for specific operations:

```go
// Suppress tracing for this context
ctx = imprint.SuppressTracing(ctx)

// Database queries with this context won't create spans
db.QueryContext(ctx, "SELECT * FROM internal_metrics")

// Check if tracing is suppressed
if imprint.IsSuppressed(ctx) {
    // Tracing is disabled for this context
}
```

Use cases:
- Internal health checks that shouldn't create spans
- High-frequency internal operations
- Avoiding trace noise from infrastructure queries

## Panic Recovery

Capture panics as errors without crashing:

```go
// Basic - re-panics after recording
handler := imprint.RecoveryMiddleware(yourHandler)

// Custom config - return 500 instead of re-panicking
handler := imprint.RecoveryMiddlewareWithConfig(yourHandler, imprint.RecoveryConfig{
    RePanic: false,
})

// Custom handler
handler := imprint.RecoveryMiddlewareFunc(yourHandler, func(w http.ResponseWriter, r *http.Request, err interface{}, stack string) {
    log.Printf("Panic: %v\n%s", err, stack)
    http.Error(w, "Internal error", 500)
})
```

## Gauge Metrics

Gauges represent numeric values that can go up or down over time. Unlike counters (which track occurrences), gauges track measurements like memory usage, queue depth, or active connections.

### Recording Gauges

```go
// Record a gauge with optional attributes
client.RecordGauge(ctx, "queue.depth", float64(queueLen), map[string]interface{}{
    "queue_name": "orders",
})

// Simple gauge without extra attributes
client.RecordGauge(ctx, "cache.hit_rate", 0.85, nil)
```

### Multi-Instance Aggregation

When you run multiple instances of your service (e.g., multiple pods in Kubernetes), the dashboard automatically shows separate lines for each instance:

```go
// Each instance automatically gets its hostname as service.instance.id
client.RecordGauge(ctx, "process.memory.rss", float64(memUsage), nil)

// Override the instance ID if needed
client.RecordGauge(ctx, "process.memory.rss", float64(memUsage), map[string]interface{}{
    "service.instance.id": "web-01",
})
```

The dashboard displays:
- **Line chart** with one line per instance
- **Legend** showing instance names (hostnames)
- **Tooltip** with formatted values (KB, MB, GB)

### Gauge vs Counter vs Event

| Type | Use Case | Dashboard Display |
|------|----------|-------------------|
| `RecordGauge()` | Measurements (memory, CPU, queue depth) | Line chart |
| `RecordEvent()` | Business events (order created, user login) | Bar chart (count) |
| Span | Operations with duration | Trace waterfall |

## Runtime Metrics

Collect Go runtime statistics (memory, goroutines, GC) automatically:

```go
import "github.com/tedo-ai/imprint-go/metrics"

// Start collector (default: every 60s)
stopMetrics := metrics.StartWithClient(imprintClient)
defer stopMetrics()

// Or with custom config
stopMetrics := metrics.StartWithConfig(imprintClient, metrics.Config{
    Interval:          30 * time.Second,
    CollectMemory:     true,
    CollectGoroutines: true,
    CollectGC:         true,
    CollectCPU:        true,
})
```

### Manual Control

For more control over the collector lifecycle:

```go
// Create collector manually
collector := metrics.NewCollector(imprintClient, metrics.DefaultConfig())

// Start collection
collector.Start()

// Collect once immediately (useful for testing)
collector.CollectOnce()

// Stop when done
collector.Stop()
```

### Collected Metrics

Each metric is emitted as a **gauge** (using `RecordGauge`) and appears as a line chart in the Events page. When running multiple instances, you'll see separate lines for each hostname.

| Metric | Description |
|--------|-------------|
| `process.runtime.go.goroutines` | Current number of goroutines |
| `process.runtime.go.num_cpu` | Number of CPUs available |
| `process.runtime.go.gomaxprocs` | GOMAXPROCS value |
| `process.runtime.go.mem.heap_alloc` | Bytes allocated on heap |
| `process.runtime.go.mem.heap_sys` | Bytes obtained from system |
| `process.runtime.go.mem.heap_idle` | Bytes in idle spans |
| `process.runtime.go.mem.heap_inuse` | Bytes in use |
| `process.runtime.go.mem.heap_objects` | Number of allocated objects |
| `process.runtime.go.mem.stack_inuse` | Stack memory in use |
| `process.runtime.go.mem.total_alloc` | Total bytes allocated |
| `process.runtime.go.gc.count` | Number of GC cycles |
| `process.runtime.go.gc.pause_total_ns` | Total GC pause time |
| `process.runtime.go.gc.last_pause_ns` | Last GC pause duration |

### Running Multiple Instances

To see multi-instance aggregation in action:

```bash
# Terminal 1
PORT=8000 go run main.go

# Terminal 2
PORT=8001 go run main.go
```

After 60 seconds, the Events page will show metrics with two lines (one per instance).

## WebSocket Instrumentation

Trace long-lived WebSocket connections:

```go
import (
    "github.com/gorilla/websocket"
    imprintws "github.com/tedo-ai/imprint-go/websocket"
)

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil { return }

    // Wrap with tracing
    wsConn := imprintws.WrapConn(conn, imprintClient, r.Context())
    defer wsConn.Close()

    for {
        messageType, data, err := conn.ReadMessage()
        if err != nil { break }

        // Trace received message
        ctx, span := wsConn.TraceReceive(imprintws.MessageType(messageType), len(data))

        // Process message...
        processMessage(ctx, data)

        span.End()
    }
}

// Sending messages
func sendMessage(wsConn *imprintws.Conn, data []byte) {
    span := wsConn.TraceSend(imprintws.TextMessage, len(data))
    err := conn.WriteMessage(websocket.TextMessage, data)
    if err != nil { span.RecordError(err) }
    span.End()
}
```

### WebSocket Configuration

```go
config := imprintws.ConnConfig{
    ConnectionName:      "WS Connect",    // Span name for connection (default)
    ReceiveName:         "WS Receive",    // Span name for receives (default)
    SendName:            "WS Send",       // Span name for sends (default)
    TraceMessages:       true,            // Enable per-message tracing (default: true)
    IncludePayloadSize:  true,            // Include message size (default: true)
    MaxPayloadSizeToLog: 1024 * 1024,     // Max size before showing "large" (1MB)
}

wsConn := imprintws.NewConn(conn, imprintClient, r.Context(), imprintws.WithConfig(config))
```

### Connection Statistics

```go
// Get connection stats
messageCount, bytesSent, bytesRecv := wsConn.Stats()

// Record custom events
wsConn.RecordEvent("user_subscribed", map[string]interface{}{
    "channel": "updates",
})

// Get trace identifiers
traceID := wsConn.TraceID()
spanID := wsConn.SpanID()
```

### Message Types

```go
imprintws.TextMessage   // 1 - UTF-8 text
imprintws.BinaryMessage // 2 - Binary data
imprintws.CloseMessage  // 8 - Close frame
imprintws.PingMessage   // 9 - Ping frame
imprintws.PongMessage   // 10 - Pong frame
```

## Sampling

Reduce trace volume while ensuring errors are captured:

```go
client := imprint.NewClient(imprint.Config{
    SamplingRate: 0.1, // Sample 10% of traces
})
```

**Key behaviors:**
- Sampling is trace-ID consistent (same decision across services)
- Child spans inherit parent's sampling decision
- Spans with `RecordError()` are **always sent** (tail-based promotion)
- Promoted spans are marked with `imprint.sampling.promoted: true`

### Custom Samplers

```go
import "github.com/tedo-ai/imprint-go/sampler"

// Always sample
client.SetSampler(sampler.AlwaysSample{})

// Never sample
client.SetSampler(sampler.NeverSample{})

// Rate-based with custom rate
client.SetSampler(sampler.NewRateSampler(0.25))
```

### Parent-Based Sampling

Defer to parent span's sampling decision with a fallback for root spans:

```go
// Use 10% sampling for root spans, inherit from parent otherwise
fallback := sampler.NewRateSampler(0.1)
client.SetSampler(sampler.NewParentBasedSampler(fallback))
```

### Composite Samplers

Combine multiple sampling strategies:

```go
// AND mode: sample only if ALL samplers agree
client.SetSampler(sampler.NewCompositeSampler(
    sampler.CompositeAND,
    sampler.NewRateSampler(0.5),   // 50% rate
    sampler.AlwaysSample{},        // Always (effectively just the rate)
))

// OR mode: sample if ANY sampler agrees
client.SetSampler(sampler.NewCompositeSampler(
    sampler.CompositeOR,
    sampler.NewRateSampler(0.1),   // 10% baseline
    customHighPrioritySampler,      // Your custom logic
))
```

### Checking Sampling Status

```go
ctx, span := client.StartSpan(ctx, "operation")

// Check if this span will be sent
if span.IsSampled() {
    // Span is sampled (or was promoted due to error)
}
```

## RUM Integration

Link backend traces to frontend Real User Monitoring:

### In Go Templates

```go
func handlePage(w http.ResponseWriter, r *http.Request) {
    span := imprint.FromContext(r.Context())

    data := struct {
        TraceParent string
    }{
        TraceParent: span.TraceParentString(),
    }

    tmpl.Execute(w, data)
}
```

```html
<head>
    <meta name="traceparent" content="{{ .TraceParent }}">
</head>
```

### TraceParentString Format

Returns W3C Trace Context format: `00-{trace_id}-{span_id}-01`

```go
span := imprint.FromContext(ctx)
traceparent := span.TraceParentString()
// "00-abc123def456...-789xyz...-01"
```

## Span Kinds

| Kind | Description | Color in Dashboard |
|------|-------------|-------------------|
| `server` | HTTP server handling request | Blue |
| `client` | Outbound call (HTTP, DB, Redis) | Purple |
| `internal` | Internal operation (default) | Gray |
| `consumer` | Message/event consumer | Purple |
| `producer` | Message/event producer | Purple |

## SDK Metadata

All spans automatically include OpenTelemetry semantic convention attributes:

| Attribute | Value |
|-----------|-------|
| `telemetry.sdk.name` | `imprint-go` |
| `telemetry.sdk.version` | `0.1.0` |
| `telemetry.sdk.language` | `go` |

These attributes help identify which SDK version generated each span, useful for debugging version-specific issues.

## Complete Example

```go
package main

import (
    "context"
    "log/slog"
    "net/http"
    "os"
    "time"

    "github.com/redis/go-redis/v9"
    imprint "github.com/tedo-ai/imprint-go"
    "github.com/tedo-ai/imprint-go/metrics"
    imprintredis "github.com/tedo-ai/imprint-go/redis"
    imprintslog "github.com/tedo-ai/imprint-go/slog"
    imprintsql "github.com/tedo-ai/imprint-go/sql"
)

func main() {
    // Initialize client
    client := imprint.NewClient(imprint.Config{
        APIKey:       os.Getenv("IMPRINT_API_KEY"),
        ServiceName:  "my-api",
        SamplingRate: 0.5,
    })
    defer client.Shutdown(context.Background())

    // Start metrics collection
    stopMetrics := metrics.StartWithClient(client)
    defer stopMetrics()

    // Setup traced HTTP client
    httpClient := imprint.NewTracedClient(client)

    // Setup traced database
    db, _ := imprintsql.Open("postgres", os.Getenv("DATABASE_URL"),
        imprintsql.WithClient(client),
        imprintsql.WithDBSystem("postgresql"),
    )

    // Setup traced Redis
    rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
    rdb.AddHook(imprintredis.NewHook(client))

    // Setup traced logger
    logger := slog.New(imprintslog.NewJSONHandler(os.Stdout, nil))

    // Setup HTTP server
    mux := http.NewServeMux()
    mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()

        logger.InfoContext(ctx, "Fetching user")

        // Check cache
        cached, _ := rdb.Get(ctx, "user:123").Result()
        if cached == "" {
            // Query database
            db.QueryRowContext(ctx, "SELECT * FROM users WHERE id = $1", 123)

            // Call external API
            httpClient.Get("https://api.example.com/enrich")
        }

        w.Write([]byte("OK"))
    })

    // Apply middleware stack
    handler := imprint.RecoveryMiddleware(client.Middleware(mux))

    http.ListenAndServe(":8000", handler)
}
```

## License

MIT License
