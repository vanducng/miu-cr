package cli

import (
	stdctx "context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type options struct {
	output  string
	timeout time.Duration
}

var version = "v0.3.0" // x-release-please-version

// Execute runs the miucr root command with args, returning a CLIError whose Exit
// code the caller (cmd/miucr) maps to the process status.
func Execute(args []string) error {
	opts := &options{output: "json", timeout: 30 * time.Second}
	root := rootCommand(opts)
	root.SetArgs(args)
	root.SilenceUsage = true
	root.SilenceErrors = true
	ctx, stop := signal.NotifyContext(stdctx.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		var already *CLIError
		if errors.As(err, &already) && already.AlreadyWritten {
			return err // command emitted its own envelope; just carry the exit code
		}
		errorWriter := io.Writer(os.Stdout)
		if isMCPServeCommand(args) {
			errorWriter = os.Stderr // keep the JSON-RPC stream on stdout clean
		}
		_ = writeError(errorWriter, commandPath(args), err)
		return err
	}
	return nil
}

func rootCommand(opts *options) *cobra.Command {
	root := &cobra.Command{
		Use:   "miucr",
		Short: "Owned local AI code-review CLI for agents",
	}
	root.PersistentFlags().StringVarP(&opts.output, "output", "o", "json", "Output format: json or pretty")
	root.PersistentFlags().DurationVar(&opts.timeout, "timeout", 30*time.Second, "Operation timeout")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch opts.output {
		case "", "json":
			prettyOutput = false
		case "pretty":
			prettyOutput = true
		default:
			return &CLIError{Code: "output.invalid_format", Message: fmt.Sprintf("unknown output format %q", opts.output), Hint: "use json or pretty", Exit: 2}
		}
		return nil
	}
	root.AddCommand(versionCommand())
	root.AddCommand(reviewCommand(opts))
	root.AddCommand(mcpCommand(opts))
	return root
}

// MCPRequest carries the resolved serve options to the injected MCPServer.
type MCPRequest struct {
	Transport string
	Version   string
	Timeout   time.Duration
	In        io.Reader
	Out       io.Writer
	Err       io.Writer
}

// MCPServer serves the review engine over MCP. The engine-backed implementation
// is injected at startup (internal/cli/wire) so cli stays below engine/store in
// the import graph.
type MCPServer interface {
	Serve(ctx stdctx.Context, req MCPRequest) error
}

var mcpServer MCPServer

// SetMCPServer wires the engine-backed MCP server. Called once from the wire
// package's init before any command runs.
func SetMCPServer(s MCPServer) { mcpServer = s }

func mcpCommand(opts *options) *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the review engine over MCP on stdio",
		RunE: func(cmd *cobra.Command, args []string) error {
			if mcpServer == nil {
				return &CLIError{Code: "mcp.not_wired", Message: "MCP server not wired", Exit: 1}
			}
			return mcpServer.Serve(cmd.Context(), MCPRequest{
				Transport: transport,
				Version:   versionString(),
				Timeout:   opts.timeout,
				In:        cmd.InOrStdin(),
				Out:       cmd.OutOrStdout(),
				Err:       cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "stdio", "MCP transport: stdio")
	return cmd
}

func isMCPServeCommand(args []string) bool {
	for _, a := range args {
		if a == "mcp" {
			return true
		}
		if !strings.HasPrefix(a, "-") {
			// first non-flag arg is the subcommand; stop after it.
			return a == "mcp"
		}
	}
	return false
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeSuccess(cmd.OutOrStdout(), "version", "version", map[string]any{"version": versionString()}, nil)
		},
	}
}

func versionString() string {
	if version != "" && !strings.HasSuffix(version, "-dev") {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func commandPath(args []string) string {
	if len(args) == 0 {
		return "miucr"
	}
	if len(args) > 2 {
		args = args[:2]
	}
	return strings.Join(args, " ")
}

func init() {
	cobra.EnableCommandSorting = false
}
