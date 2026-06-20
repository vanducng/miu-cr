// Package mcpserver exposes the review engine over Model Context Protocol on
// stdio. It mirrors miu-db's mcpserver: a generic AddTool registration, byte-
// bounded outputs, and an IOTransport so a host agent can drive review_run /
// review_get. All diagnostics go to stderr to keep the JSON-RPC stream clean.
package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/store"
)

// Deps is the aggregate the MCP tools run against: the review Engine and the
// persistence Store. Mirrors miu-db's New(services, opts) shape.
type Deps struct {
	Engine *engine.Engine
	Store  store.Store
}

// New builds the MCP server with review_run/review_get registered, for callers
// that drive transport themselves; Serve wraps it for stdio.
func New(deps Deps, opts Options) (*mcp.Server, error) {
	return newServer(deps, opts, nil)
}

// Serve runs the MCP server over stdio until ctx is done, keeping stdout pure
// JSON-RPC and sending all logs to stderr.
func Serve(
	ctx context.Context,
	deps Deps,
	opts Options,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if stdin == nil {
		return fmt.Errorf("stdin is required")
	}
	if stdout == nil {
		return fmt.Errorf("stdout is required")
	}
	if stderr == nil {
		stderr = io.Discard
	}
	opts = opts.withDefaults()
	transport, err := transportFor(opts.Transport, stdin, stdout)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server, err := newServer(deps, opts, logger)
	if err != nil {
		return err
	}
	return server.Run(ctx, transport)
}

func newServer(deps Deps, opts Options, logger *slog.Logger) (*mcp.Server, error) {
	if deps.Engine == nil {
		return nil, fmt.Errorf("review engine is required")
	}
	opts = opts.withDefaults()
	server := mcp.NewServer(&mcp.Implementation{
		Name:    opts.ImplementationName,
		Version: opts.ImplementationVersion,
	}, &mcp.ServerOptions{
		Logger: logger,
	})
	policy := newSafetyPolicy(opts)
	registerTools(server, deps, opts, policy)
	return server, nil
}
