package imprint_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	imprint "github.com/tedo-ai/imprint-go"
)

func TestNewSlogHandler_InjectsTraceContext(t *testing.T) {
	var buf bytes.Buffer
	handler := imprint.NewSlogHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(handler)

	span := &imprint.Span{TraceID: "abc123def456", SpanID: "deadbeef"}
	ctx := imprint.NewContext(context.Background(), span)

	logger.InfoContext(ctx, "test message")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log output: %v", err)
	}

	if got := entry["trace_id"]; got != "abc123def456" {
		t.Errorf("trace_id: got %v, want abc123def456", got)
	}
	if got := entry["span_id"]; got != "deadbeef" {
		t.Errorf("span_id: got %v, want deadbeef", got)
	}
	if got := entry["msg"]; got != "test message" {
		t.Errorf("msg: got %v, want test message", got)
	}
}

func TestNewSlogHandler_NoSpan_PassesThrough(t *testing.T) {
	var buf bytes.Buffer
	handler := imprint.NewSlogHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(handler)

	logger.InfoContext(context.Background(), "no span here")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log output: %v", err)
	}

	if _, ok := entry["trace_id"]; ok {
		t.Error("trace_id should not be present when no span is active")
	}
	if _, ok := entry["span_id"]; ok {
		t.Error("span_id should not be present when no span is active")
	}
	if got := entry["msg"]; got != "no span here" {
		t.Errorf("msg: got %v, want no span here", got)
	}
}

func TestNewSlogHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	handler := imprint.NewSlogHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(handler.WithAttrs([]slog.Attr{slog.String("service", "api")}))

	span := &imprint.Span{TraceID: "trace1", SpanID: "span1"}
	ctx := imprint.NewContext(context.Background(), span)
	logger.InfoContext(ctx, "with attrs")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log output: %v", err)
	}
	if got := entry["service"]; got != "api" {
		t.Errorf("service: got %v, want api", got)
	}
	if got := entry["trace_id"]; got != "trace1" {
		t.Errorf("trace_id: got %v, want trace1", got)
	}
}

func TestNewSlogHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	handler := imprint.NewSlogHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(handler.WithGroup("request"))

	span := &imprint.Span{TraceID: "trace2", SpanID: "span2"}
	ctx := imprint.NewContext(context.Background(), span)
	logger.InfoContext(ctx, "grouped", "path", "/api/v1")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log output: %v", err)
	}
	// When WithGroup is active, slog places record attrs (including injected
	// trace fields) inside the group.
	group, ok := entry["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'request' group in output, got: %v", entry)
	}
	if got := group["trace_id"]; got != "trace2" {
		t.Errorf("trace_id: got %v, want trace2", got)
	}
	if got := group["path"]; got != "/api/v1" {
		t.Errorf("path: got %v, want /api/v1", got)
	}
}
