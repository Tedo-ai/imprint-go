// Package imprintsql provides database/sql tracing instrumentation for Imprint.
// It wraps standard database drivers to automatically create spans for SQL queries.
package imprintsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"sync"

	imprint "github.com/tedo-ai/imprint-go"
)

// Option configures the traced driver.
type Option func(*tracerConfig)

type tracerConfig struct {
	client     *imprint.Client
	dbSystem   string
	recordStmt bool // Whether to record the SQL statement (default: true)
}

// WithClient sets the Imprint client for tracing.
func WithClient(c *imprint.Client) Option {
	return func(cfg *tracerConfig) {
		cfg.client = c
	}
}

// WithDBSystem sets the database system name (e.g., "postgres", "mysql", "sqlite").
func WithDBSystem(system string) Option {
	return func(cfg *tracerConfig) {
		cfg.dbSystem = system
	}
}

// WithRecordStatement controls whether SQL statements are recorded in spans.
// Default is true. Set to false to disable recording statements entirely.
func WithRecordStatement(record bool) Option {
	return func(cfg *tracerConfig) {
		cfg.recordStmt = record
	}
}

var (
	registeredDrivers = make(map[string]*tracedDriver)
	driverMu          sync.Mutex
	driverCounter     int
)

// Open opens a traced database connection. It wraps the underlying driver
// to automatically create spans for database operations.
//
// Usage:
//
//	db, err := imprintsql.Open("postgres", dsn,
//	    imprintsql.WithClient(imprintClient),
//	    imprintsql.WithDBSystem("postgres"),
//	)
func Open(driverName, dataSourceName string, opts ...Option) (*sql.DB, error) {
	cfg := &tracerConfig{
		dbSystem:   driverName, // Default to driver name
		recordStmt: true,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// Get the original driver
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}

	// Get the underlying driver
	originalDriver := db.Driver()
	db.Close()

	// Register a new traced driver with unique name
	driverMu.Lock()
	driverCounter++
	tracedDriverName := driverName + "_imprint_" + string(rune('0'+driverCounter))

	td := &tracedDriver{
		driver: originalDriver,
		config: cfg,
	}
	registeredDrivers[tracedDriverName] = td
	sql.Register(tracedDriverName, td)
	driverMu.Unlock()

	// Open connection using traced driver
	return sql.Open(tracedDriverName, dataSourceName)
}

// tracedDriver wraps a database driver to add tracing.
type tracedDriver struct {
	driver driver.Driver
	config *tracerConfig
}

func (d *tracedDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.driver.Open(name)
	if err != nil {
		return nil, err
	}
	return &tracedConn{conn: conn, config: d.config}, nil
}

// tracedConn wraps a database connection.
type tracedConn struct {
	conn   driver.Conn
	config *tracerConfig
}

func (c *tracedConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &tracedStmt{stmt: stmt, query: query, config: c.config}, nil
}

func (c *tracedConn) Close() error {
	return c.conn.Close()
}

func (c *tracedConn) Begin() (driver.Tx, error) {
	return c.conn.Begin()
}

// QueryContext implements driver.QueryerContext for direct queries.
func (c *tracedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.conn.(driver.QueryerContext)
	if !ok {
		// Fallback: driver doesn't support QueryerContext
		return nil, driver.ErrSkip
	}

	if imprint.IsSuppressed(ctx) {
		return queryer.QueryContext(ctx, query, args)
	}

	ctx, span := c.startSpan(ctx, query)
	defer span.End()

	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		span.RecordError(err)
	}
	return rows, err
}

// ExecContext implements driver.ExecerContext for direct executions.
func (c *tracedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.conn.(driver.ExecerContext)
	if !ok {
		// Fallback: driver doesn't support ExecerContext
		return nil, driver.ErrSkip
	}

	if imprint.IsSuppressed(ctx) {
		return execer.ExecContext(ctx, query, args)
	}

	ctx, span := c.startSpan(ctx, query)
	defer span.End()

	result, err := execer.ExecContext(ctx, query, args)
	if err != nil {
		span.RecordError(err)
	}
	return result, err
}

// PrepareContext implements driver.ConnPrepareContext.
func (c *tracedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	preparer, ok := c.conn.(driver.ConnPrepareContext)
	if ok {
		if imprint.IsSuppressed(ctx) {
			return preparer.PrepareContext(ctx, query)
		}
		stmt, err := preparer.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &tracedStmt{stmt: stmt, query: query, config: c.config}, nil
	}
	return c.Prepare(query)
}

// BeginTx implements driver.ConnBeginTx.
func (c *tracedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := c.conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	return c.conn.Begin()
}

// Ping implements driver.Pinger.
func (c *tracedConn) Ping(ctx context.Context) error {
	if pinger, ok := c.conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

// ResetSession implements driver.SessionResetter.
func (c *tracedConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

// CheckNamedValue implements driver.NamedValueChecker.
func (c *tracedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if checker, ok := c.conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (c *tracedConn) startSpan(ctx context.Context, query string) (context.Context, *imprint.Span) {
	if c.config.client == nil || imprint.IsSuppressed(ctx) {
		// No client configured or tracing suppressed, return a no-op span
		return ctx, &imprint.Span{}
	}

	spanName := parseSpanName(query)
	ctx, span := c.config.client.StartSpan(ctx, spanName, imprint.SpanOptions{Kind: "client"})

	span.SetAttribute("db.system", c.config.dbSystem)
	if c.config.recordStmt {
		span.SetAttribute("db.statement", query)
	}

	return ctx, span
}

// tracedStmt wraps a prepared statement.
type tracedStmt struct {
	stmt   driver.Stmt
	query  string
	config *tracerConfig
}

func (s *tracedStmt) Close() error {
	return s.stmt.Close()
}

func (s *tracedStmt) NumInput() int {
	return s.stmt.NumInput()
}

func (s *tracedStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.stmt.Exec(args)
}

func (s *tracedStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.stmt.Query(args)
}

// ExecContext implements driver.StmtExecContext.
func (s *tracedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := s.stmt.(driver.StmtExecContext)
	if !ok {
		// Convert to values and use non-context version
		values := namedValuesToValues(args)
		return s.stmt.Exec(values)
	}

	ctx, span := s.startSpan(ctx)
	defer span.End()

	result, err := execer.ExecContext(ctx, args)
	if err != nil {
		span.RecordError(err)
	}
	return result, err
}

// QueryContext implements driver.StmtQueryContext.
func (s *tracedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := s.stmt.(driver.StmtQueryContext)
	if !ok {
		// Convert to values and use non-context version
		values := namedValuesToValues(args)
		return s.stmt.Query(values)
	}

	ctx, span := s.startSpan(ctx)
	defer span.End()

	rows, err := queryer.QueryContext(ctx, args)
	if err != nil {
		span.RecordError(err)
	}
	return rows, err
}

func (s *tracedStmt) startSpan(ctx context.Context) (context.Context, *imprint.Span) {
	if s.config.client == nil || imprint.IsSuppressed(ctx) {
		return ctx, &imprint.Span{}
	}

	spanName := parseSpanName(s.query)
	ctx, span := s.config.client.StartSpan(ctx, spanName, imprint.SpanOptions{Kind: "client"})

	span.SetAttribute("db.system", s.config.dbSystem)
	if s.config.recordStmt {
		span.SetAttribute("db.statement", s.query)
	}

	return ctx, span
}

// namedValuesToValues converts NamedValue slice to Value slice.
func namedValuesToValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// SQL operation patterns for span naming
var (
	selectPattern = regexp.MustCompile(`(?i)^\s*SELECT\s+.*?\s+FROM\s+["'\x60]?(\w+)["'\x60]?`)
	insertPattern = regexp.MustCompile(`(?i)^\s*INSERT\s+INTO\s+["'\x60]?(\w+)["'\x60]?`)
	updatePattern = regexp.MustCompile(`(?i)^\s*UPDATE\s+["'\x60]?(\w+)["'\x60]?`)
	deletePattern = regexp.MustCompile(`(?i)^\s*DELETE\s+FROM\s+["'\x60]?(\w+)["'\x60]?`)
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
	for _, op := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "TRUNCATE", "BEGIN", "COMMIT", "ROLLBACK"} {
		if strings.HasPrefix(upperQuery, op) {
			return op
		}
	}

	return "DB Query"
}
