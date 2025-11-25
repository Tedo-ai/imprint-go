// Package redis provides tracing instrumentation for github.com/redis/go-redis/v9.
//
// Usage:
//
//	import (
//	    "github.com/redis/go-redis/v9"
//	    imprint "github.com/tedo-ai/imprint-go"
//	    imprintredis "github.com/tedo-ai/imprint-go/redis"
//	)
//
//	client := imprint.NewClient(imprint.Config{...})
//	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
//	rdb.AddHook(imprintredis.NewHook(client))
//
// All Redis commands will now be traced as child spans with kind=client.
package redis

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/redis/go-redis/v9"
	imprint "github.com/tedo-ai/imprint-go"
)

// Hook implements redis.Hook to trace Redis commands.
type Hook struct {
	client *imprint.Client

	// Options for customizing the hook behavior
	opts HookOptions
}

// HookOptions configures the Redis tracing hook.
type HookOptions struct {
	// DBSystem is the database system name for span attributes.
	// Defaults to "redis".
	DBSystem string

	// IncludeArgs determines whether to include command arguments (keys) in traces.
	// Only the first argument (typically the key) is included for privacy.
	// Defaults to true.
	IncludeArgs bool
}

// DefaultHookOptions returns sensible defaults for the hook.
func DefaultHookOptions() HookOptions {
	return HookOptions{
		DBSystem:    "redis",
		IncludeArgs: true,
	}
}

// NewHook creates a new Redis tracing hook with default options.
func NewHook(client *imprint.Client) *Hook {
	return NewHookWithOptions(client, DefaultHookOptions())
}

// NewHookWithOptions creates a new Redis tracing hook with custom options.
func NewHookWithOptions(client *imprint.Client, opts HookOptions) *Hook {
	if opts.DBSystem == "" {
		opts.DBSystem = "redis"
	}
	return &Hook{
		client: client,
		opts:   opts,
	}
}

// DialHook is called when establishing a connection.
// We don't trace dial operations by default.
func (h *Hook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

// ProcessHook traces individual Redis commands.
func (h *Hook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.client == nil {
			return next(ctx, cmd)
		}

		// Build span name: "redis {COMMAND}"
		cmdName := strings.ToUpper(cmd.Name())
		spanName := fmt.Sprintf("redis %s", cmdName)

		// Start span
		ctx, span := h.client.StartSpan(ctx, spanName, imprint.SpanOptions{Kind: "client"})
		defer span.End()

		// Set standard database attributes
		span.SetAttribute("db.system", h.opts.DBSystem)
		span.SetAttribute("db.operation", cmdName)

		// Build statement (command + key only, for privacy)
		statement := h.buildStatement(cmd)
		span.SetAttribute("db.statement", statement)

		// Execute the command
		err := next(ctx, cmd)

		// Record error if any
		if err != nil && err != redis.Nil {
			span.RecordError(err)
			span.SetAttribute("error", true)
		}

		return err
	}
}

// ProcessPipelineHook traces pipelined Redis commands.
func (h *Hook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if h.client == nil || len(cmds) == 0 {
			return next(ctx, cmds)
		}

		// Build span name for pipeline
		spanName := fmt.Sprintf("redis PIPELINE (%d commands)", len(cmds))

		// Start span
		ctx, span := h.client.StartSpan(ctx, spanName, imprint.SpanOptions{Kind: "client"})
		defer span.End()

		// Set standard database attributes
		span.SetAttribute("db.system", h.opts.DBSystem)
		span.SetAttribute("db.operation", "PIPELINE")
		span.SetAttribute("redis.pipeline.length", len(cmds))

		// Build summary of commands (just command names for pipeline)
		var cmdNames []string
		for _, cmd := range cmds {
			cmdNames = append(cmdNames, strings.ToUpper(cmd.Name()))
		}
		span.SetAttribute("db.statement", strings.Join(cmdNames, ", "))

		// Execute the pipeline
		err := next(ctx, cmds)

		// Record error if any
		if err != nil {
			span.RecordError(err)
			span.SetAttribute("error", true)
		}

		// Count errors in pipeline results
		var errCount int
		for _, cmd := range cmds {
			if cmd.Err() != nil && cmd.Err() != redis.Nil {
				errCount++
			}
		}
		if errCount > 0 {
			span.SetAttribute("redis.pipeline.errors", errCount)
		}

		return err
	}
}

// buildStatement creates a safe statement string from a command.
// Only includes the command name and the first argument (typically the key).
// Values are not included for privacy reasons.
func (h *Hook) buildStatement(cmd redis.Cmder) string {
	args := cmd.Args()
	if len(args) == 0 {
		return ""
	}

	// First arg is the command name
	cmdName := strings.ToUpper(fmt.Sprintf("%v", args[0]))

	if !h.opts.IncludeArgs || len(args) < 2 {
		return cmdName
	}

	// Second arg is typically the key - include it
	key := fmt.Sprintf("%v", args[1])

	// Truncate very long keys
	if len(key) > 100 {
		key = key[:100] + "..."
	}

	return fmt.Sprintf("%s %s", cmdName, key)
}

// WrapClient is a convenience function that adds the tracing hook to a Redis client.
// It returns the same client for method chaining.
func WrapClient(rdb *redis.Client, imprintClient *imprint.Client) *redis.Client {
	rdb.AddHook(NewHook(imprintClient))
	return rdb
}

// WrapClusterClient is a convenience function that adds the tracing hook to a Redis cluster client.
func WrapClusterClient(rdb *redis.ClusterClient, imprintClient *imprint.Client) *redis.ClusterClient {
	rdb.AddHook(NewHook(imprintClient))
	return rdb
}
