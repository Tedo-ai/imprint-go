package slog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	imprint "github.com/tedo-ai/imprint-go"
	imprintslog "github.com/tedo-ai/imprint-go/slog"
)

func TestHandler_InjectsTraceContext(t *testing.T) {
	var buf bytes.Buffer
	h := imprintslog.NewHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(h)

	span := &imprint.Span{TraceID: "aabbccdd", SpanID: "11223344"}
	ctx := imprint.NewContext(context.Background(), span)
	logger.InfoContext(ctx, "hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := entry["trace_id"]; got != "aabbccdd" {
		t.Errorf("trace_id: got %v, want aabbccdd", got)
	}
	if got := entry["span_id"]; got != "11223344" {
		t.Errorf("span_id: got %v, want 11223344", got)
	}
}

func TestHandler_NoSpan_PassesThrough(t *testing.T) {
	var buf bytes.Buffer
	h := imprintslog.NewHandler(slog.NewJSONHandler(&buf, nil))
	slog.New(h).InfoContext(context.Background(), "no span")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := entry["trace_id"]; ok {
		t.Error("trace_id should be absent without active span")
	}
	if _, ok := entry["span_id"]; ok {
		t.Error("span_id should be absent without active span")
	}
}

func TestHandler_CustomKeys(t *testing.T) {
	var buf bytes.Buffer
	opts := imprintslog.DefaultHandlerOptions()
	opts.TraceIDKey = "tid"
	opts.SpanIDKey = "sid"
	h := imprintslog.NewHandlerWithOptions(slog.NewJSONHandler(&buf, nil), opts)
	logger := slog.New(h)

	span := &imprint.Span{TraceID: "t1", SpanID: "s1"}
	ctx := imprint.NewContext(context.Background(), span)
	logger.InfoContext(ctx, "custom keys")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := entry["tid"]; got != "t1" {
		t.Errorf("tid: got %v, want t1", got)
	}
	if got := entry["sid"]; got != "s1" {
		t.Errorf("sid: got %v, want s1", got)
	}
	if _, ok := entry["trace_id"]; ok {
		t.Error("default trace_id key should not appear when custom key is set")
	}
}

func TestHandler_AddTraceParent(t *testing.T) {
	var buf bytes.Buffer
	opts := imprintslog.DefaultHandlerOptions()
	opts.AddTraceParent = true
	h := imprintslog.NewHandlerWithOptions(slog.NewJSONHandler(&buf, nil), opts)
	logger := slog.New(h)

	span := &imprint.Span{TraceID: "traceid128", SpanID: "spanid64"}
	ctx := imprint.NewContext(context.Background(), span)
	logger.InfoContext(ctx, "with traceparent")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := "00-traceid128-spanid64-01"
	if got := entry["traceparent"]; got != want {
		t.Errorf("traceparent: got %v, want %v", got, want)
	}
}
