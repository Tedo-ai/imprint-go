// Package websocket provides tracing instrumentation for WebSocket connections.
//
// WebSockets are long-lived connections that don't fit the standard request/response
// model. This package provides helpers to trace:
// - Connection establishment (WS Connect span)
// - Individual messages (WS Receive/WS Send spans)
// - Connection lifecycle
//
// Usage with gorilla/websocket:
//
//	import (
//	    "github.com/gorilla/websocket"
//	    imprint "github.com/tedo-ai/imprint-go"
//	    imprintws "github.com/tedo-ai/imprint-go/websocket"
//	)
//
//	func handleWebSocket(w http.ResponseWriter, r *http.Request) {
//	    // Upgrade connection
//	    conn, err := upgrader.Upgrade(w, r, nil)
//	    if err != nil { return }
//
//	    // Wrap with tracing
//	    ctx := r.Context()
//	    wsConn := imprintws.WrapConn(conn, imprintClient, ctx)
//	    defer wsConn.Close()
//
//	    // Read messages (each creates a span)
//	    for {
//	        messageType, data, err := wsConn.ReadMessage()
//	        if err != nil { break }
//	        // Process message...
//	    }
//	}
package websocket

import (
	"context"
	"fmt"
	"sync"

	imprint "github.com/tedo-ai/imprint-go"
)

// MessageType represents WebSocket message types.
type MessageType int

const (
	TextMessage   MessageType = 1
	BinaryMessage MessageType = 2
	CloseMessage  MessageType = 8
	PingMessage   MessageType = 9
	PongMessage   MessageType = 10
)

// String returns a human-readable name for the message type.
func (mt MessageType) String() string {
	switch mt {
	case TextMessage:
		return "text"
	case BinaryMessage:
		return "binary"
	case CloseMessage:
		return "close"
	case PingMessage:
		return "ping"
	case PongMessage:
		return "pong"
	default:
		return fmt.Sprintf("unknown(%d)", mt)
	}
}

// ConnConfig holds configuration for WebSocket connection tracing.
type ConnConfig struct {
	// ConnectionName is the name for the connection span. Default: "WS Connect".
	ConnectionName string

	// ReceiveName is the name for receive spans. Default: "WS Receive".
	ReceiveName string

	// SendName is the name for send spans. Default: "WS Send".
	SendName string

	// TraceMessages enables per-message tracing. Default: true.
	TraceMessages bool

	// IncludePayloadSize includes message size in span attributes. Default: true.
	IncludePayloadSize bool

	// MaxPayloadSizeToLog is the max payload size to include in attributes.
	// Payloads larger than this show "large" instead. Default: 1MB.
	MaxPayloadSizeToLog int
}

// DefaultConnConfig returns sensible defaults.
func DefaultConnConfig() ConnConfig {
	return ConnConfig{
		ConnectionName:      "WS Connect",
		ReceiveName:         "WS Receive",
		SendName:            "WS Send",
		TraceMessages:       true,
		IncludePayloadSize:  true,
		MaxPayloadSizeToLog: 1024 * 1024, // 1MB
	}
}

// Conn wraps a WebSocket connection with tracing capabilities.
// It provides methods for traced message reading/writing.
type Conn struct {
	// Underlying connection (can be *websocket.Conn from gorilla/websocket)
	underlying interface{}

	client      *imprint.Client
	config      ConnConfig
	connSpan    *imprint.Span
	connCtx     context.Context
	connTraceID string
	connSpanID  string

	mu           sync.Mutex
	messageCount int64
	bytesSent    int64
	bytesRecv    int64
	closed       bool
}

// ConnOption is a function that configures a Conn.
type ConnOption func(*Conn)

// WithConfig sets a custom configuration.
func WithConfig(cfg ConnConfig) ConnOption {
	return func(c *Conn) {
		c.config = cfg
	}
}

// NewConn creates a new traced WebSocket connection wrapper.
// The underlying parameter should be your actual WebSocket connection
// (e.g., *websocket.Conn from gorilla/websocket).
// The ctx should come from the HTTP request that initiated the upgrade.
func NewConn(underlying interface{}, client *imprint.Client, ctx context.Context, opts ...ConnOption) *Conn {
	c := &Conn{
		underlying: underlying,
		client:     client,
		config:     DefaultConnConfig(),
	}

	for _, opt := range opts {
		opt(c)
	}

	// Start connection span
	c.connCtx, c.connSpan = client.StartSpan(ctx, c.config.ConnectionName, imprint.SpanOptions{Kind: "server"})
	c.connTraceID = c.connSpan.TraceID
	c.connSpanID = c.connSpan.SpanID

	c.connSpan.SetAttribute("messaging.system", "websocket")
	c.connSpan.SetAttribute("messaging.operation", "connect")

	return c
}

// WrapConn is a convenience function that creates a traced connection wrapper.
func WrapConn(underlying interface{}, client *imprint.Client, ctx context.Context) *Conn {
	return NewConn(underlying, client, ctx)
}

// Context returns the connection's context with trace information.
func (c *Conn) Context() context.Context {
	return c.connCtx
}

// TraceID returns the trace ID for this connection.
func (c *Conn) TraceID() string {
	return c.connTraceID
}

// SpanID returns the connection span ID.
func (c *Conn) SpanID() string {
	return c.connSpanID
}

// Underlying returns the underlying WebSocket connection.
func (c *Conn) Underlying() interface{} {
	return c.underlying
}

// TraceReceive creates a span for receiving a message.
// Call this before reading a message from your WebSocket connection.
// Returns a context and span that should be ended after processing.
//
// Example:
//
//	ctx, span := wsConn.TraceReceive(websocket.TextMessage, len(data))
//	defer span.End()
//	// process message...
func (c *Conn) TraceReceive(messageType MessageType, size int) (context.Context, *imprint.Span) {
	if !c.config.TraceMessages || c.client == nil {
		return context.Background(), &imprint.Span{}
	}

	c.mu.Lock()
	c.messageCount++
	c.bytesRecv += int64(size)
	msgNum := c.messageCount
	c.mu.Unlock()

	// Create a new root span for the message (consumer pattern)
	// We link back to the connection span via attributes
	ctx, span := c.client.StartSpan(context.Background(), c.config.ReceiveName, imprint.SpanOptions{Kind: "consumer"})

	span.SetAttribute("messaging.system", "websocket")
	span.SetAttribute("messaging.operation", "receive")
	span.SetAttribute("messaging.message_type", messageType.String())
	span.SetAttribute("messaging.message_id", fmt.Sprintf("%s-%d", c.connSpanID, msgNum))

	// Link to connection span
	span.SetAttribute("messaging.connection.trace_id", c.connTraceID)
	span.SetAttribute("messaging.connection.span_id", c.connSpanID)

	if c.config.IncludePayloadSize {
		if size > c.config.MaxPayloadSizeToLog {
			span.SetAttribute("messaging.message.body_size", "large")
		} else {
			span.SetAttribute("messaging.message.body_size", size)
		}
	}

	return ctx, span
}

// TraceSend creates a span for sending a message.
// Call this before writing a message to your WebSocket connection.
// Returns a span that should be ended after sending.
//
// Example:
//
//	span := wsConn.TraceSend(websocket.TextMessage, len(data))
//	err := conn.WriteMessage(websocket.TextMessage, data)
//	if err != nil { span.RecordError(err) }
//	span.End()
func (c *Conn) TraceSend(messageType MessageType, size int) *imprint.Span {
	if !c.config.TraceMessages || c.client == nil {
		return &imprint.Span{}
	}

	c.mu.Lock()
	c.bytesSent += int64(size)
	c.mu.Unlock()

	// Send spans are children of the connection context
	_, span := c.client.StartSpan(c.connCtx, c.config.SendName, imprint.SpanOptions{Kind: "producer"})

	span.SetAttribute("messaging.system", "websocket")
	span.SetAttribute("messaging.operation", "send")
	span.SetAttribute("messaging.message_type", messageType.String())

	if c.config.IncludePayloadSize {
		if size > c.config.MaxPayloadSizeToLog {
			span.SetAttribute("messaging.message.body_size", "large")
		} else {
			span.SetAttribute("messaging.message.body_size", size)
		}
	}

	return span
}

// RecordError records an error on the connection span.
func (c *Conn) RecordError(err error) {
	if c.connSpan != nil {
		c.connSpan.RecordError(err)
	}
}

// Close ends the connection span with final statistics.
// You should still close the underlying connection separately.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	messageCount := c.messageCount
	bytesSent := c.bytesSent
	bytesRecv := c.bytesRecv
	c.mu.Unlock()

	if c.connSpan != nil {
		c.connSpan.SetAttribute("messaging.message_count", messageCount)
		c.connSpan.SetAttribute("messaging.bytes_sent", bytesSent)
		c.connSpan.SetAttribute("messaging.bytes_received", bytesRecv)
		c.connSpan.End()
	}

	return nil
}

// Stats returns connection statistics.
func (c *Conn) Stats() (messageCount, bytesSent, bytesRecv int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.messageCount, c.bytesSent, c.bytesRecv
}

// RecordEvent records a custom event in the connection's trace context.
func (c *Conn) RecordEvent(name string, attributes map[string]interface{}) {
	if c.client != nil {
		c.client.RecordEvent(c.connCtx, name, attributes)
	}
}
