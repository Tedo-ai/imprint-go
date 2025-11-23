package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	imprint "github.com/tedo-ai/imprint-go"
	imprintsql "github.com/tedo-ai/imprint-go/sql"

	_ "github.com/mattn/go-sqlite3"
)

var templates *template.Template
var imprintClient *imprint.Client
var config Config
var db *sql.DB

type Config struct {
	APIKey    string
	IngestURL string
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

func main() {
	// Load .env file
	loadEnv()

	// Load configuration
	config = Config{
		APIKey:    getEnv("IMPRINT_API_KEY", "test-key"),
		IngestURL: getEnv("IMPRINT_INGEST_URL", "http://localhost:8080/v1/spans"),
	}

	// Initialize Imprint client
	imprintClient = imprint.NewClient(imprint.Config{
		APIKey:      config.APIKey,
		ServiceName: "imprint-demo",
		IngestURL:   config.IngestURL,
	})

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
	mux.HandleFunc("/panic", handlePanic)

	// Wrap with Imprint middleware
	handler := imprintClient.Middleware(mux)

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
	// Create a child span before panicking
	_, span := imprintClient.StartSpan(r.Context(), "about_to_panic")
	span.RecordError(nil) // Mark as error
	span.End()

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
