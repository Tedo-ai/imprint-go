package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	imprint "github.com/tedo-ai/imprint-go"
	"github.com/tedo-ai/imprint-go/metrics"
	imprintredis "github.com/tedo-ai/imprint-go/redis"
	imprintslog "github.com/tedo-ai/imprint-go/slog"
	imprintsql "github.com/tedo-ai/imprint-go/sql"

	_ "github.com/mattn/go-sqlite3"
)

// tracedHTTPClient is the http.Client wrapped with Imprint tracing
var tracedHTTPClient *http.Client

// rdb is the traced Redis client
var rdb *redis.Client

// logger is the trace-aware slog logger
var logger *slog.Logger

// stopMetrics stops the metrics collector
var stopMetrics func()

var templates *template.Template
var imprintClient *imprint.Client
var config Config
var db *sql.DB

type Config struct {
	APIKey       string
	IngestURL    string
	RedisAddr    string
	SamplingRate float64
}

func loadEnv() {
	file, err := os.Open(".env")
	if err != nil {
		log.Printf("Warning: .env file not found, using defaults")
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			os.Setenv(parts[0], parts[1])
		}
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// corsMiddleware adds CORS headers to allow browser requests with traceparent
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, traceparent, tracestate")
		w.Header().Set("Access-Control-Expose-Headers", "traceparent, tracestate")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	// Load .env file
	loadEnv()

	// Load configuration
	// Parse sampling rate from env (default 0.5 = 50% of traces)
	samplingRate := 0.5
	if envRate := getEnv("IMPRINT_SAMPLING_RATE", ""); envRate != "" {
		fmt.Sscanf(envRate, "%f", &samplingRate)
	}

	config = Config{
		APIKey:       getEnv("IMPRINT_API_KEY", "test-key"),
		IngestURL:    getEnv("IMPRINT_INGEST_URL", "http://localhost:8080/v1/spans"),
		RedisAddr:    getEnv("REDIS_ADDR", "localhost:6379"),
		SamplingRate: samplingRate,
	}

	// Initialize Imprint client with sampling and metrics enabled
	imprintClient = imprint.NewClient(imprint.Config{
		APIKey:       config.APIKey,
		ServiceName:  "imprint-demo",
		IngestURL:    config.IngestURL,
		SamplingRate: config.SamplingRate, // 0.5 = ~50% of traces sampled
	})

	// Start runtime metrics collector (reports every 60s)
	stopMetrics = metrics.StartWithConfig(imprintClient, metrics.Config{
		Interval:          60 * time.Second,
		CollectMemory:     true,
		CollectGoroutines: true,
		CollectGC:         true,
		CollectCPU:        true,
	})

	log.Printf("Sampling rate: %.0f%% (errors always captured)", config.SamplingRate*100)

	// Create a traced HTTP client for outbound requests
	tracedHTTPClient = imprint.NewTracedClient(imprintClient)

	// Initialize trace-aware slog logger
	// This automatically adds trace_id and span_id to all log messages
	logger = slog.New(imprintslog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger) // Make it the default logger

	// Initialize traced Redis client
	rdb = redis.NewClient(&redis.Options{
		Addr: config.RedisAddr,
	})
	rdb.AddHook(imprintredis.NewHook(imprintClient))

	// Test Redis connection (non-fatal if unavailable)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("Warning: Redis not available at %s: %v (Redis features will fail)", config.RedisAddr, err)
	} else {
		log.Printf("Connected to Redis at %s", config.RedisAddr)
	}
	cancel()

	// Initialize traced database connection
	var err error
	db, err = imprintsql.Open("sqlite3", ":memory:",
		imprintsql.WithClient(imprintClient),
		imprintsql.WithDBSystem("sqlite"),
	)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	// Create demo table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	// Seed some demo data
	db.Exec(`INSERT INTO items (name) VALUES ('Widget A'), ('Widget B'), ('Widget C')`)

	// Parse templates
	templates, err = template.ParseGlob("templates/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	// Create router
	mux := http.NewServeMux()

	// Static files
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Routes
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/about", handleAbout)
	mux.HandleFunc("/api/data", handleAPIData)
	mux.HandleFunc("/api/validate", handleValidate)
	mux.HandleFunc("/api/checkout", handleCheckout)
	mux.HandleFunc("/api/payment", handlePayment)
	mux.HandleFunc("/api/external", handleExternalAPI)
	mux.HandleFunc("/api/enqueue", handleEnqueueJob)
	mux.HandleFunc("/api/process-job", handleProcessJob)
	mux.HandleFunc("/api/cache", handleCache)
	mux.HandleFunc("/api/user", handleUserWithLogs)
	mux.HandleFunc("/panic", handlePanic)

	// Wrap with CORS middleware (must be before Imprint to allow traceparent header)
	corsHandler := corsMiddleware(mux)
	// Wrap with Imprint middleware
	tracedHandler := imprintClient.Middleware(corsHandler)
	// Wrap with panic recovery middleware (records panics to spans, then re-panics)
	handler := imprint.RecoveryMiddlewareWithConfig(tracedHandler, imprint.RecoveryConfig{
		RePanic: false, // Don't re-panic, just return 500 for the demo
	})

	// Create server
	server := &http.Server{
		Addr:    ":8000",
		Handler: handler,
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("Shutting down server...")

		// Stop metrics collector
		if stopMetrics != nil {
			stopMetrics()
		}

		// Shutdown Imprint client (flushes remaining spans)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := imprintClient.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down Imprint client: %v", err)
		}

		// Shutdown HTTP server
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down server: %v", err)
		}
	}()

	log.Printf("Starting imprint-demo server on http://localhost:8000")
	log.Printf("Using Imprint ingest URL: %s", config.IngestURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

type PageData struct {
	APIKey    string
	IngestURL string
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Create a child span for template rendering
	ctx, span := imprintClient.StartSpan(r.Context(), "render_template:index")
	defer span.End()

	// Record a page view event (appears as a dot in the waterfall)
	imprintClient.RecordEvent(ctx, "page_view", map[string]interface{}{
		"page":       "index",
		"user_agent": r.UserAgent(),
		"referrer":   r.Referer(),
	})

	// Query database to get item count (traced automatically)
	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items").Scan(&count)
	if err != nil {
		span.RecordError(err)
	}

	// Record item count as a metric event
	imprintClient.RecordEvent(ctx, "items_counted", map[string]interface{}{
		"count": count,
	})

	data := PageData{
		APIKey:    config.APIKey,
		IngestURL: config.IngestURL,
	}

	if err := templates.ExecuteTemplate(w, "index.html", data); err != nil {
		span.RecordError(err)
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}
}

func handleAbout(w http.ResponseWriter, r *http.Request) {
	// Create a child span for template rendering
	ctx, span := imprintClient.StartSpan(r.Context(), "render_template:about")
	defer span.End()

	// Query database for latest item (traced automatically)
	var name string
	_ = db.QueryRowContext(ctx, "SELECT name FROM items ORDER BY id DESC LIMIT 1").Scan(&name)

	data := PageData{
		APIKey:    config.APIKey,
		IngestURL: config.IngestURL,
	}

	if err := templates.ExecuteTemplate(w, "about.html", data); err != nil {
		span.RecordError(err)
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}
}

func handleAPIData(w http.ResponseWriter, r *http.Request) {
	// Create a child span for API processing
	ctx, span := imprintClient.StartSpan(r.Context(), "process_api_request")
	defer span.End()

	// Query all items from database (traced automatically)
	rows, err := db.QueryContext(ctx, "SELECT id, name, created_at FROM items")
	if err != nil {
		span.RecordError(err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	type Item struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
	}

	var items []Item
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatedAt); err != nil {
			span.RecordError(err)
			continue
		}
		items = append(items, item)
	}
	rows.Close() // Explicitly close before recording event

	// Record business event: items fetched (strictly AFTER SELECT completes)
	imprintClient.RecordEvent(ctx, "items_fetched", map[string]interface{}{
		"count": len(items),
	})

	// Insert a new item to demonstrate INSERT tracing
	newItemName := "Item-" + time.Now().Format("15:04:05")
	result, err := db.ExecContext(ctx, "INSERT INTO items (name) VALUES (?)", newItemName)
	if err != nil {
		span.RecordError(err)
	}

	// Record business event: item created (strictly AFTER INSERT completes)
	if err == nil {
		lastID, _ := result.LastInsertId()
		imprintClient.RecordEvent(ctx, "item_created", map[string]interface{}{
			"item_id":   lastID,
			"item_name": newItemName,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "hello",
		"items":   items,
	})
}

// handleValidate demonstrates using RecordEvent for validation errors
// Try: curl "http://localhost:8000/api/validate?email=invalid&name="
// Or:  curl "http://localhost:8000/api/validate?email=user@example.com&name=John"
func handleValidate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	email := r.URL.Query().Get("email")
	name := r.URL.Query().Get("name")

	var errors []string

	// Validate email
	if email == "" {
		imprintClient.RecordEvent(ctx, "validation_error", map[string]interface{}{
			"field":  "email",
			"reason": "required",
		})
		errors = append(errors, "email is required")
	} else if !strings.Contains(email, "@") {
		imprintClient.RecordEvent(ctx, "validation_error", map[string]interface{}{
			"field":  "email",
			"reason": "invalid_format",
			"value":  email,
		})
		errors = append(errors, "email must contain @")
	}

	// Validate name
	if name == "" {
		imprintClient.RecordEvent(ctx, "validation_error", map[string]interface{}{
			"field":  "name",
			"reason": "required",
		})
		errors = append(errors, "name is required")
	} else if len(name) < 2 {
		imprintClient.RecordEvent(ctx, "validation_error", map[string]interface{}{
			"field":     "name",
			"reason":    "too_short",
			"min_length": 2,
		})
		errors = append(errors, "name must be at least 2 characters")
	}

	w.Header().Set("Content-Type", "application/json")

	if len(errors) > 0 {
		// Record summary event for failed validation
		imprintClient.RecordEvent(ctx, "validation_failed", map[string]interface{}{
			"error_count": len(errors),
		})
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"errors":  errors,
		})
		return
	}

	// Record success event
	imprintClient.RecordEvent(ctx, "validation_passed", map[string]interface{}{
		"email": email,
		"name":  name,
	})

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Validation passed!",
		"data": map[string]string{
			"email": email,
			"name":  name,
		},
	})
}

func handlePanic(w http.ResponseWriter, r *http.Request) {
	// The RecoveryMiddleware will catch this panic, record it to the span,
	// and return a 500 response (since we configured RePanic: false)
	panic("intentional panic for testing error capture")
}

// handleCheckout demonstrates rich custom attributes on a span
// POST /api/checkout - Simulates a checkout transaction with user context
func handleCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, span := imprintClient.StartSpan(r.Context(), "process_checkout")
	defer span.End()

	// Set rich custom attributes
	span.SetAttribute("user_id", "user_123")
	span.SetAttribute("plan", "pro")
	span.SetAttribute("cart_total", "49.99")
	span.SetAttribute("currency", "USD")
	span.SetAttribute("item_count", 3)

	// Simulate database insert for order
	orderID := time.Now().UnixNano()
	_, err := db.ExecContext(ctx, "INSERT INTO items (name) VALUES (?)", fmt.Sprintf("Order-%d", orderID))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(500)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Record business event
	imprintClient.RecordEvent(ctx, "order_created", map[string]interface{}{
		"order_id":   orderID,
		"user_id":    "user_123",
		"cart_total": 49.99,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"order_id": orderID,
		"message":  "Checkout completed successfully",
	})
}

// handlePayment demonstrates error recording without panicking
// POST /api/payment - Simulates a payment failure
func handlePayment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, span := imprintClient.StartSpan(r.Context(), "process_payment")
	defer span.End()

	// Set payment context
	span.SetAttribute("user_id", "user_123")
	span.SetAttribute("payment_method", "credit_card")
	span.SetAttribute("amount", "49.99")

	// Simulate payment processing
	imprintClient.RecordEvent(ctx, "payment_initiated", map[string]interface{}{
		"amount":         49.99,
		"payment_method": "credit_card",
	})

	// Simulate a payment failure (insufficient funds)
	err := fmt.Errorf("insufficient funds: available balance $12.50, required $49.99")

	// Record the error on the span (this sets error_data)
	span.RecordError(err)
	span.SetStatus(402)

	// Record business event for the failure
	imprintClient.RecordEvent(ctx, "payment_failed", map[string]interface{}{
		"reason": "insufficient_funds",
		"amount": 49.99,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"error":   "Payment failed: insufficient funds",
		"code":    "INSUFFICIENT_FUNDS",
	})
}

// handleExternalAPI demonstrates outbound HTTP tracing with WrapClient.
// GET /api/external - Makes traced HTTP calls to external APIs.
// This demonstrates the Transport/WrapClient feature for tracing outbound calls.
func handleExternalAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Create a child span for the overall operation
	ctx, span := imprintClient.StartSpan(ctx, "fetch_external_data")
	defer span.End()

	// Make a traced HTTP request to an external API (httpbin.org echo service)
	// The tracedHTTPClient automatically:
	// 1. Creates a child span (kind=client)
	// 2. Injects traceparent header
	// 3. Records status code
	req, err := http.NewRequestWithContext(ctx, "GET", "https://httpbin.org/get", nil)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// Record that we're about to call external API
	imprintClient.RecordEvent(ctx, "external_api_call_start", map[string]interface{}{
		"url":    "https://httpbin.org/get",
		"method": "GET",
	})

	resp, err := tracedHTTPClient.Do(req)
	if err != nil {
		span.RecordError(err)
		imprintClient.RecordEvent(ctx, "external_api_call_failed", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(w, fmt.Sprintf("External API call failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "Failed to read response", http.StatusInternalServerError)
		return
	}

	// Record success
	imprintClient.RecordEvent(ctx, "external_api_call_success", map[string]interface{}{
		"status_code":   resp.StatusCode,
		"response_size": len(body),
	})

	// Return the external API response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":            "Successfully called external API",
		"external_status":    resp.StatusCode,
		"external_response":  json.RawMessage(body),
		"trace_was_injected": resp.Request.Header.Get("traceparent") != "",
	})
}

// Simulated job queue (in-memory for demo purposes)
var jobQueue = make(chan map[string]interface{}, 100)

// handleEnqueueJob demonstrates context propagation for async job queues.
// POST /api/enqueue - Enqueues a job with trace context for later processing.
// This demonstrates the Inject function for serializing trace context.
func handleEnqueueJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Create a span for the enqueue operation
	ctx, span := imprintClient.StartSpan(ctx, "enqueue_job")
	defer span.End()

	// Create job payload
	jobData := map[string]interface{}{
		"job_id":     fmt.Sprintf("job_%d", time.Now().UnixNano()),
		"job_type":   "process_order",
		"created_at": time.Now().Format(time.RFC3339),
		"payload": map[string]interface{}{
			"order_id": "12345",
			"user_id":  "user_123",
		},
	}

	// Inject trace context into the job payload
	// This allows the worker to continue the same trace
	traceCarrier := make(map[string]string)
	imprint.Inject(ctx, traceCarrier)
	jobData["_trace"] = traceCarrier

	span.SetAttribute("job_id", jobData["job_id"])
	span.SetAttribute("job_type", "process_order")

	// Simulate enqueueing to a job queue
	select {
	case jobQueue <- jobData:
		imprintClient.RecordEvent(ctx, "job_enqueued", map[string]interface{}{
			"job_id":   jobData["job_id"],
			"job_type": "process_order",
		})
	default:
		span.RecordError(fmt.Errorf("job queue full"))
		http.Error(w, "Job queue full", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"job_id":        jobData["job_id"],
		"message":       "Job enqueued successfully",
		"trace_context": traceCarrier,
	})
}

// handleProcessJob demonstrates extracting trace context from a job payload.
// POST /api/process-job - Simulates a worker processing a job from the queue.
// This demonstrates the Extract function for deserializing trace context.
func handleProcessJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Try to get a job from the queue
	var jobData map[string]interface{}
	select {
	case jobData = <-jobQueue:
		// Got a job
	default:
		http.Error(w, "No jobs in queue", http.StatusNotFound)
		return
	}

	// Extract trace context from the job payload
	var ctx context.Context
	if traceData, ok := jobData["_trace"].(map[string]string); ok {
		ctx = imprint.Extract(traceData)
	} else {
		ctx = context.Background()
	}

	// Start a span for processing - this will be a child of the original request
	ctx, span := imprintClient.StartSpan(ctx, "process_job")
	defer span.End()

	jobID, _ := jobData["job_id"].(string)
	span.SetAttribute("job_id", jobID)
	span.SetAttribute("job_type", "process_order")

	imprintClient.RecordEvent(ctx, "job_processing_started", map[string]interface{}{
		"job_id": jobID,
	})

	// Simulate some processing work
	time.Sleep(100 * time.Millisecond)

	// Simulate a database operation
	_, err := db.ExecContext(ctx, "INSERT INTO items (name) VALUES (?)", fmt.Sprintf("ProcessedJob-%s", jobID))
	if err != nil {
		span.RecordError(err)
	}

	imprintClient.RecordEvent(ctx, "job_processing_completed", map[string]interface{}{
		"job_id":       jobID,
		"duration_ms":  100,
		"items_created": 1,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"job_id":  jobID,
		"message": "Job processed successfully (trace linked to original request)",
	})
}

// handleCache demonstrates Redis tracing.
// GET /api/cache?key=mykey - Get a cached value
// POST /api/cache?key=mykey&value=myvalue - Set a cached value
// This demonstrates the Redis hook that traces all Redis operations.
func handleCache(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.URL.Query().Get("key")
	if key == "" {
		key = "demo:counter"
	}

	// Log with trace context - this will automatically include trace_id and span_id
	logger.InfoContext(ctx, "Cache operation started",
		"key", key,
		"method", r.Method,
	)

	if r.Method == http.MethodPost {
		// Set a value
		value := r.URL.Query().Get("value")
		if value == "" {
			value = fmt.Sprintf("value_%d", time.Now().UnixNano())
		}

		// This Redis SET will be traced automatically
		err := rdb.Set(ctx, key, value, 5*time.Minute).Err()
		if err != nil {
			logger.ErrorContext(ctx, "Redis SET failed", "error", err, "key", key)
			http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			return
		}

		logger.InfoContext(ctx, "Cache value set",
			"key", key,
			"value", value,
			"ttl", "5m",
		)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"operation": "SET",
			"key":       key,
			"value":     value,
		})
		return
	}

	// GET - retrieve value and increment counter
	// This Redis GET will be traced automatically
	value, err := rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		logger.WarnContext(ctx, "Cache miss", "key", key)

		// Increment a miss counter (traced)
		rdb.Incr(ctx, "demo:cache_misses")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"operation": "GET",
			"key":       key,
			"value":     nil,
			"cache_hit": false,
		})
		return
	} else if err != nil {
		logger.ErrorContext(ctx, "Redis GET failed", "error", err, "key", key)
		http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
		return
	}

	// Increment hit counter (traced)
	rdb.Incr(ctx, "demo:cache_hits")

	logger.InfoContext(ctx, "Cache hit",
		"key", key,
		"value_length", len(value),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"operation": "GET",
		"key":       key,
		"value":     value,
		"cache_hit": true,
	})
}

// handleUserWithLogs demonstrates slog integration with trace correlation.
// GET /api/user?id=123 - Simulates user operations with detailed logging.
// All log messages will include trace_id and span_id automatically.
func handleUserWithLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := r.URL.Query().Get("id")
	if userID == "" {
		userID = "123"
	}

	// Start a child span for user processing
	ctx, span := imprintClient.StartSpan(ctx, "process_user")
	defer span.End()

	span.SetAttribute("user_id", userID)

	// All these logs will automatically include trace_id and span_id
	logger.InfoContext(ctx, "Starting user processing",
		"user_id", userID,
		"request_path", r.URL.Path,
	)

	// Simulate checking cache (Redis traced)
	cacheKey := fmt.Sprintf("user:%s:profile", userID)
	cached, err := rdb.Get(ctx, cacheKey).Result()

	if err == redis.Nil {
		logger.DebugContext(ctx, "User profile cache miss, fetching from database",
			"user_id", userID,
			"cache_key", cacheKey,
		)

		// Simulate database lookup (SQL traced)
		var itemCount int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM items").Scan(&itemCount)
		if err != nil {
			logger.ErrorContext(ctx, "Database query failed",
				"user_id", userID,
				"error", err,
			)
			span.RecordError(err)
		}

		// Cache the result (Redis traced)
		profile := fmt.Sprintf(`{"user_id":"%s","name":"Demo User","items":%d}`, userID, itemCount)
		if err := rdb.Set(ctx, cacheKey, profile, 1*time.Minute).Err(); err != nil {
			logger.WarnContext(ctx, "Failed to cache user profile",
				"user_id", userID,
				"error", err,
			)
		}

		logger.InfoContext(ctx, "User profile loaded from database and cached",
			"user_id", userID,
			"item_count", itemCount,
		)

		cached = profile
	} else if err != nil {
		logger.ErrorContext(ctx, "Redis error looking up user cache",
			"user_id", userID,
			"error", err,
		)
		http.Error(w, "Cache error", http.StatusInternalServerError)
		return
	} else {
		logger.InfoContext(ctx, "User profile loaded from cache",
			"user_id", userID,
		)
	}

	// Record an event for metrics
	imprintClient.RecordEvent(ctx, "user_profile_accessed", map[string]interface{}{
		"user_id": userID,
	})

	logger.InfoContext(ctx, "User processing completed",
		"user_id", userID,
	)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(cached))
}
