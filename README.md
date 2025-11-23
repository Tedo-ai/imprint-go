# Imprint Go SDK

The official Go client for [Imprint](https://github.com/tedo-ai/imprint). This SDK allows you to instrument your Go applications to send traces to the Imprint ingestion API.

## Installation

```bash
go get github.com/tedo-ai/imprint-go
```

## Usage

### 1. Initialize the Client

Initialize the client at the start of your application (e.g., in `main.go`).

```go
package main

import (
    "context"
    "net/http"
    "github.com/tedo-ai/imprint-go"
)

func main() {
    // Configure and create the client
    client := imprint.NewClient(imprint.Config{
        APIKey:      "imp_live_xxxxxxxxxxxx", // Your Project API Key
        ServiceName: "my-service",           // Name of this service
        IngestURL:   "http://localhost:8080/v1/traces", // Imprint Ingest URL
    })
    
    // Ensure spans are flushed before exit
    defer client.Shutdown(context.Background())

    // ... your application code
}
```

### 2. HTTP Middleware

The easiest way to instrument your application is using the provided middleware. It automatically:
- Creates a span for each request.
- Captures the HTTP method, path, and status code.
- Propagates the trace context from incoming headers (`traceparent`).

```go
    mux := http.NewServeMux()
    mux.HandleFunc("/hello", helloHandler)

    // Wrap your router/mux with the middleware
    http.ListenAndServe(":8000", client.Middleware(mux))
```

### 3. Manual Instrumentation

You can also manually start and end spans, or add attributes to the current span.

```go
func helloHandler(w http.ResponseWriter, r *http.Request) {
    // Retrieve the current span from context (injected by middleware)
    span := imprint.FromContext(r.Context())
    
    if span != nil {
        // Add custom attributes
        span.SetAttribute("user_id", "12345")
        span.SetAttribute("plan", "premium")
    }

    // Create a child span for a specific operation
    ctx, childSpan := client.StartSpan(r.Context(), "database_query")
    defer childSpan.End()
    
    // Simulate work...
    // db.Query(...)
    
    w.Write([]byte("Hello World"))
}
```

## Span Kinds

The SDK supports OpenTelemetry span kinds to categorize operations:

- **`server`**: HTTP server handling an incoming request (automatically set by middleware)
- **`client`**: Outbound call to external service or database
- **`internal`**: Internal operation (default)

### Setting Span Kind

The middleware automatically sets `kind="server"` for HTTP requests. For custom spans, you can specify the kind:

```go
// Database query span
ctx, dbSpan := client.StartSpan(r.Context(), "SELECT users", imprint.SpanOptions{
    Kind: "client",
})
defer dbSpan.End()

// External API call
ctx, apiSpan := client.StartSpan(ctx, "POST /external/api", imprint.SpanOptions{
    Kind: "client",
})
defer apiSpan.End()
```

The span kind is used in the Imprint Dashboard for color-coding:
- **Blue**: Server spans
- **Purple**: Client spans (external calls)
- **Yellow**: Client spans (database queries, detected by name heuristics)

## Filtering Requests

To reduce noise and save costs, you can configure the SDK to ignore certain requests:

```go
client := imprint.NewClient(imprint.Config{
    APIKey:      "imp_live_xxxxxxxxxxxx",
    ServiceName: "my-service",
    IngestURL:   "http://localhost:8080/v1/traces",
    
    // Ignore specific paths
    IgnorePaths: []string{"/health", "/metrics", "/favicon.ico"},
    
    // Ignore path prefixes (e.g., static assets)
    IgnorePrefixes: []string{"/static/", "/assets/", "/public/"},
    
    // Ignore file extensions (optional - defaults to common static files)
    IgnoreExtensions: []string{".map"}, // Add to default list
})
```

### Default Behavior

If you don't specify `IgnoreExtensions`, the SDK automatically ignores common static file types:
- `.css`, `.js`, `.png`, `.jpg`, `.jpeg`, `.gif`, `.ico`, `.svg`
- `.woff`, `.woff2`, `.ttf`, `.eot`, `.map`

To disable this default behavior, pass an empty slice: `IgnoreExtensions: []string{}`.

### How It Works

The middleware checks each request against the ignore rules **before** creating a span:
1. **Exact Match**: If the path exactly matches an entry in `IgnorePaths`, it's ignored.
2. **Prefix Match**: If the path starts with any entry in `IgnorePrefixes`, it's ignored.
3. **Extension Match**: If the path ends with any entry in `IgnoreExtensions`, it's ignored.

Ignored requests are passed through to your handler without any tracing overhead.



## Configuration

| Field | Type | Description |
|-------|------|-------------|
| `APIKey` | `string` | **Required**. Your project's API key. |
| `ServiceName` | `string` | **Required**. Identifier for this service (e.g., "api-server"). |
| `IngestURL` | `string` | URL of the Imprint Ingest API. Defaults to `http://localhost:8080/v1/traces`. |
