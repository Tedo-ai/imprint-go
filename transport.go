package imprint

import (
	"fmt"
	"net/http"
)

// Transport implements http.RoundTripper to automatically trace outbound HTTP requests.
// It creates child spans for each request with kind=client and propagates trace context
// via the W3C traceparent header.
type Transport struct {
	// Base is the underlying RoundTripper. If nil, http.DefaultTransport is used.
	Base http.RoundTripper

	// Client is the Imprint client used to create spans.
	Client *Client
}

// RoundTrip implements http.RoundTripper. It creates a span for the outgoing request,
// injects the traceparent header, and records the response status.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Get the base transport
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	// If no client configured, just pass through
	if t.Client == nil {
		return base.RoundTrip(req)
	}

	// Start a child span
	ctx := req.Context()
	spanName := fmt.Sprintf("HTTP %s", req.Method)
	ctx, span := t.Client.StartSpan(ctx, spanName, SpanOptions{Kind: "client"})
	defer span.End()

	// Set attributes
	span.SetAttribute("http.method", req.Method)
	span.SetAttribute("http.url", req.URL.String())
	if req.URL.Host != "" {
		span.SetAttribute("http.host", req.URL.Host)
	}

	// Inject traceparent header for distributed tracing
	// Format: 00-{trace_id}-{span_id}-01
	traceparent := span.TraceParentString()
	req = req.Clone(ctx) // Clone to avoid mutating the original request
	req.Header.Set("traceparent", traceparent)

	// Execute the request
	resp, err := base.RoundTrip(req)

	// Record error if request failed
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("http.error", err.Error())
		return resp, err
	}

	// Record response status
	span.SetStatus(uint16(resp.StatusCode))
	span.SetAttribute("http.status_code", resp.StatusCode)

	return resp, nil
}

// WrapClient wraps an existing http.Client with tracing capabilities.
// If the client is nil, a new http.Client is created with the tracing transport.
// The returned client will automatically trace all outgoing HTTP requests.
func WrapClient(client *http.Client, imprintClient *Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}

	// Get the existing transport or use default
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	// Wrap with our tracing transport
	client.Transport = &Transport{
		Base:   base,
		Client: imprintClient,
	}

	return client
}

// NewTracedClient creates a new http.Client with tracing enabled.
// This is a convenience function equivalent to WrapClient(nil, imprintClient).
func NewTracedClient(imprintClient *Client) *http.Client {
	return WrapClient(nil, imprintClient)
}
