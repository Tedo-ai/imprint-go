package imprint

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Middleware returns a http.Handler middleware that traces requests.
func (c *Client) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this request should be ignored
		if shouldIgnore(r.URL.Path, c.config) {
			next.ServeHTTP(w, r)
			return
		}

		// Check for incoming traceparent header
		// Format: 00-traceid-spanid-flags
		// We only care about traceid and spanid for parenting
		var parentSpan *Span
		traceParent := r.Header.Get("traceparent")
		if traceParent != "" {
			parts := strings.Split(traceParent, "-")
			if len(parts) == 4 {
				parentSpan = &Span{
					TraceID: parts[1],
					SpanID:  parts[2],
				}
			}
		}

		ctx := r.Context()
		if parentSpan != nil {
			ctx = NewContext(ctx, parentSpan)
		}

		// Create new span
		ctx, span := c.StartSpan(ctx, fmt.Sprintf("%s %s", r.Method, r.URL.Path), SpanOptions{Kind: "server"})
		defer span.End()

		// Wrap ResponseWriter to capture status code
		rw := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		// Inject context
		next.ServeHTTP(rw, r.WithContext(ctx))

		// Record status code
		span.StatusCode = uint16(rw.statusCode)
		if rw.statusCode >= 500 {
			span.RecordError(nil) // Just mark as error-ish if needed, or rely on status code
		}
	})
}

// shouldIgnore determines if a request path should be ignored based on the config.
func shouldIgnore(path string, config Config) bool {
	// Check exact path matches
	for _, ignorePath := range config.IgnorePaths {
		if path == ignorePath {
			return true
		}
	}

	// Check path prefixes
	for _, prefix := range config.IgnorePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	// Check file extensions
	// If user didn't specify extensions, use sensible defaults
	extensions := config.IgnoreExtensions
	if len(extensions) == 0 {
		extensions = []string{".css", ".js", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".svg", ".woff", ".woff2", ".ttf", ".eot", ".map"}
	}

	for _, ext := range extensions {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}

	return false
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseRecorder) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker interface for WebSocket support.
// This allows the underlying connection to be taken over for protocols
// that require direct connection access (WebSockets, HTTP/2 server push, etc.)
func (rw *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("ResponseWriter does not support hijacking")
}

// Flush implements http.Flusher interface for streaming support.
// This is needed for Server-Sent Events (SSE) and other streaming responses.
func (rw *responseRecorder) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
