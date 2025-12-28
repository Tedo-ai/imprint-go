package imprint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// LogEntry represents a log record with trace correlation support.
type LogEntry struct {
	Timestamp  time.Time         `json:"timestamp"`
	TraceID    string            `json:"trace_id,omitempty"`
	SpanID     string            `json:"span_id,omitempty"`
	Severity   string            `json:"severity"`
	Message    string            `json:"message"`
	Namespace  string            `json:"namespace"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Severity levels following OpenTelemetry conventions.
const (
	SeverityDebug = "debug"
	SeverityInfo  = "info"
	SeverityWarn  = "warn"
	SeverityError = "error"
	SeverityFatal = "fatal"
)

// RecordLog records a log entry with automatic trace correlation.
// If called within a traced context, the log is automatically linked
// to the current trace and span.
//
// Usage:
//
//	client.RecordLog(ctx, imprint.SeverityInfo, "User signed in",
//	    map[string]interface{}{"user_id": 123, "plan": "premium"})
//
//	client.RecordLog(ctx, imprint.SeverityError, "Payment failed",
//	    map[string]interface{}{"order_id": "ord_123", "amount": 99.99})
func (c *Client) RecordLog(ctx context.Context, severity, message string, attributes map[string]interface{}) {
	// Get trace context if available
	var traceID, spanID string
	if span := FromContext(ctx); span != nil {
		traceID = span.TraceID
		spanID = span.SpanID
	}

	// Convert attributes to string values
	attrs := make(map[string]string, len(attributes)+3)
	for k, v := range attributes {
		attrs[k] = fmt.Sprintf("%v", v)
	}

	// Add SDK metadata
	attrs["telemetry.sdk.name"] = SDKName
	attrs["telemetry.sdk.version"] = SDKVersion
	attrs["telemetry.sdk.language"] = SDKLanguage

	entry := LogEntry{
		Timestamp:  time.Now(),
		TraceID:    traceID,
		SpanID:     spanID,
		Severity:   normalizeSeverity(severity),
		Message:    message,
		Namespace:  c.config.ServiceName,
		Attributes: attrs,
	}

	// Queue for batch sending
	select {
	case c.logChan <- entry:
	default:
		// Buffer full, drop log
	}
}

// Debug logs a debug-level message.
func (c *Client) Debug(ctx context.Context, message string, attributes map[string]interface{}) {
	c.RecordLog(ctx, SeverityDebug, message, attributes)
}

// Info logs an info-level message.
func (c *Client) Info(ctx context.Context, message string, attributes map[string]interface{}) {
	c.RecordLog(ctx, SeverityInfo, message, attributes)
}

// Warn logs a warning-level message.
func (c *Client) Warn(ctx context.Context, message string, attributes map[string]interface{}) {
	c.RecordLog(ctx, SeverityWarn, message, attributes)
}

// Error logs an error-level message.
func (c *Client) Error(ctx context.Context, message string, attributes map[string]interface{}) {
	c.RecordLog(ctx, SeverityError, message, attributes)
}

// Fatal logs a fatal-level message.
func (c *Client) Fatal(ctx context.Context, message string, attributes map[string]interface{}) {
	c.RecordLog(ctx, SeverityFatal, message, attributes)
}

// normalizeSeverity converts various severity strings to standard values.
func normalizeSeverity(s string) string {
	switch strings.ToLower(s) {
	case "debug", "trace":
		return SeverityDebug
	case "info", "information":
		return SeverityInfo
	case "warn", "warning":
		return SeverityWarn
	case "error", "err":
		return SeverityError
	case "fatal", "critical", "panic":
		return SeverityFatal
	default:
		return SeverityInfo
	}
}

// logWorker runs in a goroutine to batch and send logs.
func (c *Client) logWorker() {
	defer c.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var buffer []LogEntry

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		c.sendLogBatch(buffer)
		buffer = nil
		buffer = make([]LogEntry, 0, 100)
	}

	for {
		select {
		case entry := <-c.logChan:
			buffer = append(buffer, entry)
			if len(buffer) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-c.stopChan:
			// Drain channel
			for {
				select {
				case entry := <-c.logChan:
					buffer = append(buffer, entry)
				default:
					flush()
					return
				}
			}
		}
	}
}

// sendLogBatch sends a batch of logs to the ingest endpoint.
func (c *Client) sendLogBatch(logs []LogEntry) {
	payload, err := json.Marshal(logs)
	if err != nil {
		fmt.Printf("Error marshaling logs: %v\n", err)
		return
	}

	// Build logs URL from spans URL
	logsURL := strings.Replace(c.config.IngestURL, "/v1/spans", "/v1/logs", 1)

	req, err := http.NewRequest("POST", logsURL, bytes.NewReader(payload))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Printf("Error sending logs: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fmt.Printf("Logs API returned error: %d\n", resp.StatusCode)
	} else {
		fmt.Printf("Successfully sent batch of %d logs\n", len(logs))
	}
}
