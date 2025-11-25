package imprint

import (
	"fmt"
	"net/http"
	"runtime/debug"
)

// RecoveryConfig holds configuration for the panic recovery middleware.
type RecoveryConfig struct {
	// RePanic determines whether to re-panic after recording the error.
	// If true (default), the panic is re-raised after being recorded,
	// allowing upstream recovery handlers to also process it.
	// If false, the middleware sends a 500 response and swallows the panic.
	RePanic bool
}

// DefaultRecoveryConfig returns the default configuration for recovery middleware.
// By default, panics are re-raised after being recorded.
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		RePanic: true,
	}
}

// RecoveryMiddleware returns an http.Handler that recovers from panics,
// records them to the current span, and either re-panics or returns a 500 response.
// This is the simplest form - it re-panics by default to allow upstream handlers
// to also catch the panic.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return RecoveryMiddlewareWithConfig(next, DefaultRecoveryConfig())
}

// RecoveryMiddlewareWithConfig returns an http.Handler that recovers from panics
// with custom configuration.
func RecoveryMiddlewareWithConfig(next http.Handler, config RecoveryConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				// Get the stack trace
				stack := string(debug.Stack())

				// Try to get the current span from context
				span := FromContext(r.Context())
				if span != nil {
					// Record the panic as an error
					panicErr := fmt.Errorf("panic: %v", err)
					span.RecordError(panicErr)

					// Set error attributes with stack trace
					span.SetAttribute("error.type", "panic")
					span.SetAttribute("error.stack", stack)
					span.SetAttribute("error.message", fmt.Sprintf("%v", err))

					// Mark status as 500
					span.SetStatus(500)
				}

				if config.RePanic {
					// Re-panic to allow upstream handlers to catch it
					panic(err)
				}

				// If not re-panicking, send a 500 response
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// RecoveryMiddlewareFunc is a convenience function that creates recovery middleware
// with a custom panic handler function. The handler receives the recovered value
// and the stack trace.
func RecoveryMiddlewareFunc(next http.Handler, handler func(w http.ResponseWriter, r *http.Request, err interface{}, stack string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				stack := string(debug.Stack())

				// Try to record to span
				span := FromContext(r.Context())
				if span != nil {
					panicErr := fmt.Errorf("panic: %v", err)
					span.RecordError(panicErr)
					span.SetAttribute("error.type", "panic")
					span.SetAttribute("error.stack", stack)
					span.SetAttribute("error.message", fmt.Sprintf("%v", err))
					span.SetStatus(500)
				}

				// Call the custom handler
				handler(w, r, err, stack)
			}
		}()

		next.ServeHTTP(w, r)
	})
}
