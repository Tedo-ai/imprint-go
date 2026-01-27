// Package imprintpgx provides pgx/pgxpool tracing instrumentation for Imprint.
// It implements pgx's tracer interface to automatically create spans for SQL queries.
package imprintpgx

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	imprint "github.com/tedo-ai/imprint-go"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey int

const spanKey contextKey = iota

// Tracer implements pgx.QueryTracer to automatically trace database queries.
type Tracer struct {
	client       *imprint.Client
	recordStmt   bool
	maxStmtLen   int
	sanitizeStmt bool
}

// Option configures the tracer.
type Option func(*Tracer)

// WithRecordStatement controls whether SQL statements are recorded in spans.
// Default is true. Set to false to disable recording statements for security.
func WithRecordStatement(record bool) Option {
	return func(t *Tracer) {
		t.recordStmt = record
	}
}

// WithMaxStatementLength sets the maximum length of SQL statements to record.
// Statements longer than this will be truncated with "..." appended.
// Default is 0 (no limit). Recommended: 2000 for most use cases.
func WithMaxStatementLength(maxLen int) Option {
	return func(t *Tracer) {
		t.maxStmtLen = maxLen
	}
}

// WithSanitizeStatement enables sanitization of SQL parameters in recorded statements.
// When enabled, parameter values are replaced with placeholders to avoid logging
// sensitive data like passwords, tokens, or PII.
// Default is false (parameters are recorded as-is).
func WithSanitizeStatement(sanitize bool) Option {
	return func(t *Tracer) {
		t.sanitizeStmt = sanitize
	}
}

// NewTracer creates a new pgx tracer that sends spans to Imprint.
func NewTracer(client *imprint.Client, opts ...Option) *Tracer {
	t := &Tracer{
		client:     client,
		recordStmt: true,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// NewPool creates a new pgxpool.Pool with Imprint tracing enabled.
// This is the recommended way to create a traced pool.
//
// Usage:
//
//	pool, err := imprintpgx.NewPool(ctx, databaseURL, imprintClient)
func NewPool(ctx context.Context, connString string, client *imprint.Client, opts ...Option) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}

	tracer := NewTracer(client, opts...)
	config.ConnConfig.Tracer = tracer

	return pgxpool.NewWithConfig(ctx, config)
}

// NewPoolWithConfig creates a new pgxpool.Pool from existing config with tracing.
// Use this when you need to customize pool settings.
//
// Usage:
//
//	config, _ := pgxpool.ParseConfig(databaseURL)
//	config.MaxConns = 25
//	pool, err := imprintpgx.NewPoolWithConfig(ctx, config, imprintClient)
func NewPoolWithConfig(ctx context.Context, config *pgxpool.Config, client *imprint.Client, opts ...Option) (*pgxpool.Pool, error) {
	tracer := NewTracer(client, opts...)
	config.ConnConfig.Tracer = tracer
	return pgxpool.NewWithConfig(ctx, config)
}

// TraceQueryStart implements pgx.QueryTracer.
func (t *Tracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if t.client == nil {
		return ctx
	}

	spanName := parseSpanName(data.SQL)
	ctx, span := t.client.StartSpan(ctx, spanName, imprint.SpanOptions{Kind: "client"})

	span.SetAttribute("db.system", "postgresql")
	if t.recordStmt {
		stmt := data.SQL
		if t.sanitizeStmt {
			stmt = sanitizeSQL(stmt)
		}
		if t.maxStmtLen > 0 && len(stmt) > t.maxStmtLen {
			stmt = stmt[:t.maxStmtLen] + "..."
		}
		span.SetAttribute("db.statement", stmt)
	}

	return context.WithValue(ctx, spanKey, span)
}

// TraceQueryEnd implements pgx.QueryTracer.
func (t *Tracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(spanKey).(*imprint.Span)
	if !ok || span == nil {
		return
	}

	if data.Err != nil {
		span.RecordError(data.Err)
	}

	// Record command tag if available (e.g., "SELECT 5", "INSERT 0 1")
	if data.CommandTag.String() != "" {
		span.SetAttribute("db.rows_affected", data.CommandTag.RowsAffected())
	}

	span.End()
}

// TraceBatchStart implements pgx.BatchTracer (optional).
func (t *Tracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	if t.client == nil {
		return ctx
	}

	ctx, span := t.client.StartSpan(ctx, "BATCH", imprint.SpanOptions{Kind: "client"})
	span.SetAttribute("db.system", "postgresql")
	span.SetAttribute("db.batch_size", data.Batch.Len())

	return context.WithValue(ctx, spanKey, span)
}

// TraceBatchQuery implements pgx.BatchTracer (optional).
func (t *Tracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	// Individual batch queries are traced within the batch span
	// We could create child spans here if needed
}

// TraceBatchEnd implements pgx.BatchTracer (optional).
func (t *Tracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	span, ok := ctx.Value(spanKey).(*imprint.Span)
	if !ok || span == nil {
		return
	}

	if data.Err != nil {
		span.RecordError(data.Err)
	}

	span.End()
}

// TraceCopyFromStart implements pgx.CopyFromTracer (optional).
func (t *Tracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	if t.client == nil {
		return ctx
	}

	spanName := "COPY " + data.TableName.Sanitize()
	ctx, span := t.client.StartSpan(ctx, spanName, imprint.SpanOptions{Kind: "client"})
	span.SetAttribute("db.system", "postgresql")
	span.SetAttribute("db.operation", "COPY")
	span.SetAttribute("db.sql.table", data.TableName.Sanitize())

	return context.WithValue(ctx, spanKey, span)
}

// TraceCopyFromEnd implements pgx.CopyFromTracer (optional).
func (t *Tracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	span, ok := ctx.Value(spanKey).(*imprint.Span)
	if !ok || span == nil {
		return
	}

	if data.Err != nil {
		span.RecordError(data.Err)
	}

	span.SetAttribute("db.rows_affected", data.CommandTag.RowsAffected())
	span.End()
}

// TracePrepareStart implements pgx.PrepareTracer (optional).
func (t *Tracer) TracePrepareStart(ctx context.Context, _ *pgx.Conn, data pgx.TracePrepareStartData) context.Context {
	if t.client == nil {
		return ctx
	}

	spanName := "PREPARE " + data.Name
	ctx, span := t.client.StartSpan(ctx, spanName, imprint.SpanOptions{Kind: "client"})
	span.SetAttribute("db.system", "postgresql")
	span.SetAttribute("db.operation", "PREPARE")
	if t.recordStmt {
		stmt := data.SQL
		if t.sanitizeStmt {
			stmt = sanitizeSQL(stmt)
		}
		if t.maxStmtLen > 0 && len(stmt) > t.maxStmtLen {
			stmt = stmt[:t.maxStmtLen] + "..."
		}
		span.SetAttribute("db.statement", stmt)
	}

	return context.WithValue(ctx, spanKey, span)
}

// TracePrepareEnd implements pgx.PrepareTracer (optional).
func (t *Tracer) TracePrepareEnd(ctx context.Context, _ *pgx.Conn, data pgx.TracePrepareEndData) {
	span, ok := ctx.Value(spanKey).(*imprint.Span)
	if !ok || span == nil {
		return
	}

	if data.Err != nil {
		span.RecordError(data.Err)
	}

	span.End()
}

// TraceConnectStart implements pgx.ConnectTracer (optional).
func (t *Tracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	if t.client == nil {
		return ctx
	}

	ctx, span := t.client.StartSpan(ctx, "CONNECT", imprint.SpanOptions{Kind: "client"})
	span.SetAttribute("db.system", "postgresql")
	span.SetAttribute("db.operation", "CONNECT")

	return context.WithValue(ctx, spanKey, span)
}

// TraceConnectEnd implements pgx.ConnectTracer (optional).
func (t *Tracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	span, ok := ctx.Value(spanKey).(*imprint.Span)
	if !ok || span == nil {
		return
	}

	if data.Err != nil {
		span.RecordError(data.Err)
	}

	span.End()
}

// SQL operation patterns for span naming
var (
	selectPattern = regexp.MustCompile(`(?i)^\s*SELECT\s+.*?\s+FROM\s+["'\x60]?(\w+)["'\x60]?`)
	insertPattern = regexp.MustCompile(`(?i)^\s*INSERT\s+INTO\s+["'\x60]?(\w+)["'\x60]?`)
	updatePattern = regexp.MustCompile(`(?i)^\s*UPDATE\s+["'\x60]?(\w+)["'\x60]?`)
	deletePattern = regexp.MustCompile(`(?i)^\s*DELETE\s+FROM\s+["'\x60]?(\w+)["'\x60]?`)
)

// sanitizeSQL replaces parameter values in SQL with placeholders.
// This is a best-effort sanitization for common SQL patterns.
func sanitizeSQL(sql string) string {
	// Replace string literals 'value' with '?'
	sql = stringLiteralPattern.ReplaceAllString(sql, "'?'")
	// Replace numeric values that look like parameters
	sql = numericPattern.ReplaceAllString(sql, "$1?")
	return sql
}

// SQL sanitization patterns
var (
	// Match string literals: 'anything' including escaped quotes
	stringLiteralPattern = regexp.MustCompile(`'(?:[^'\\]|\\.)*'`)
	// Match numeric values after operators or keywords
	numericPattern = regexp.MustCompile(`(=\s*|,\s*|\(\s*|VALUES\s*\()(\d+\.?\d*)`)
)

// parseSpanName extracts a meaningful span name from a SQL query.
// Returns format: "OPERATION table_name" (e.g., "SELECT users")
func parseSpanName(query string) string {
	query = strings.TrimSpace(query)

	if matches := selectPattern.FindStringSubmatch(query); len(matches) > 1 {
		return "SELECT " + matches[1]
	}
	if matches := insertPattern.FindStringSubmatch(query); len(matches) > 1 {
		return "INSERT " + matches[1]
	}
	if matches := updatePattern.FindStringSubmatch(query); len(matches) > 1 {
		return "UPDATE " + matches[1]
	}
	if matches := deletePattern.FindStringSubmatch(query); len(matches) > 1 {
		return "DELETE " + matches[1]
	}

	// Fallback: use first word of query
	upperQuery := strings.ToUpper(query)
	for _, op := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "TRUNCATE", "BEGIN", "COMMIT", "ROLLBACK", "WITH"} {
		if strings.HasPrefix(upperQuery, op) {
			return op
		}
	}

	return "DB Query"
}
