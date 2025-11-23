package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	imprint "github.com/tedo-ai/imprint-go"
)

func main() {
	// Initialize the Imprint client with ignore rules
	client := imprint.NewClient(imprint.Config{
		APIKey:      "imp_live_xxxxxxxxxxxx", // Replace with your API key
		ServiceName: "demo-app",
		IngestURL:   "http://localhost:8080/v1/traces",

		// Ignore health checks and metrics
		IgnorePaths: []string{"/health", "/metrics"},

		// Ignore static assets
		IgnorePrefixes: []string{"/static/"},

		// Default extensions (.css, .js, etc.) are automatically ignored
		// Add custom extensions if needed:
		// IgnoreExtensions: []string{".map"},
	})
	defer client.Shutdown(context.Background())

	mux := http.NewServeMux()

	// Regular endpoints (will be traced)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello World"))
	})

	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		// Simulate some work
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte(`{"data": "example"}`))
	})

	// Health check (will be ignored)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	// Metrics endpoint (will be ignored)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("metrics data"))
	})

	// Static file (will be ignored due to prefix)
	mux.HandleFunc("/static/imprint.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte("console.log('Imprint SDK');"))
	})

	fmt.Println("Demo app running on :8000")
	fmt.Println("Try these requests:")
	fmt.Println("  curl http://localhost:8000/         (TRACED)")
	fmt.Println("  curl http://localhost:8000/api/data (TRACED)")
	fmt.Println("  curl http://localhost:8000/health   (IGNORED)")
	fmt.Println("  curl http://localhost:8000/metrics  (IGNORED)")
	fmt.Println("  curl http://localhost:8000/static/imprint.js (IGNORED)")

	http.ListenAndServe(":8000", client.Middleware(mux))
}
