package cli

import (
	stdctx "context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/serve"
)

type options struct {
	output  string
	timeout time.Duration
}

var version = "v0.4.0" // x-release-please-version

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
	root.AddCommand(serveCommand(opts))
	root.AddCommand(rulesCommand(opts))
	return root
}

// serveCommand runs the webhook daemon. It fails fast on misconfiguration: an
// empty WEBHOOK_SECRET accepts forged webhooks (serve.secret_required), no token
// can't post or clone (serve.token_required), and an empty allowlist would deny
// everything so --repos is required. Each job's reviewFn delegates to the
// in-process M2 path via ReviewPRForServe (Post:true); all serve-side errors are
// routed through config.RedactString inside the serve package.
func serveCommand(opts *options) *cobra.Command {
	var (
		addr  string
		gate  string
		repos []string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HMAC webhook daemon that reviews PRs on push",
		RunE: func(cmd *cobra.Command, args []string) error {
			secret := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET"))
			if secret == "" {
				return &CLIError{
					Code:    "serve.secret_required",
					Message: "WEBHOOK_SECRET is required: an empty secret would accept forged webhooks",
					Hint:    "set WEBHOOK_SECRET to the shared HMAC secret configured on the GitHub webhook",
					Exit:    2,
				}
			}
			token := resolveGitHubToken("")
			if token == "" {
				return &CLIError{
					Code:    "serve.token_required",
					Message: "a GitHub token is required: set GITHUB_TOKEN or GH_TOKEN",
					Hint:    "create a PAT with repo scope so serve can clone and post reviews",
					Exit:    2,
				}
			}
			if len(repos) == 0 {
				return &CLIError{
					Code:    "serve.repos_required",
					Message: "--repos is required: an empty allowlist reviews nothing",
					Hint:    "pass --repos owner/repo[,owner/repo...] to allow specific repositories",
					Exit:    2,
				}
			}

			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))
			reviewTO := 15 * time.Minute
			gateVal := gate
			reviewFn := func(j serve.Job) {
				// Detach from cmd.Context() (the SIGTERM signal context): on shutdown
				// it cancels immediately and would abort every in-flight review before
				// serve.Run's graceful drain can finish. j.Timeout still bounds the job.
				jobCtx := stdctx.Background()
				if j.Timeout > 0 {
					var cancel stdctx.CancelFunc
					jobCtx, cancel = stdctx.WithTimeout(jobCtx, j.Timeout)
					defer cancel()
				}
				out, err := ReviewPRForServe(jobCtx, PRReviewRequest{
					Ref:     j.Ref,
					Token:   j.Token,
					Post:    true,
					Gate:    gateVal,
					Timeout: j.Timeout,
				})
				if err != nil {
					log.Error("review failed", "ref", j.Ref, "err", config.RedactString(err.Error()))
					return
				}
				posted, action := 0, "none"
				if out.PR != nil {
					posted, action = out.PR.PostedInline, out.PR.SummaryAction
				}
				log.Info("review done", "ref", j.Ref, "findings", len(out.Findings), "posted_inline", posted, "summary", action)
			}
			srv, pool, err := serve.New(serve.Config{
				Addr:          addr,
				Secret:        []byte(secret),
				Repos:         repos,
				ResolveToken:  func() (string, error) { return resolveGitHubToken(""), nil },
				Logger:        log,
				ReviewTimeout: reviewTO,
			}, reviewFn)
			if err != nil {
				return &CLIError{Code: "serve.config_invalid", Message: err.Error(), Exit: 2}
			}
			return srv.Run(cmd.Context(), pool)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "Listen address for the webhook server")
	cmd.Flags().StringVar(&gate, "gate", "high", "Publish-severity gate for posted reviews (publish-only; never affects serve liveness)")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Required owner/repo allowlist (comma-separated); webhooks for other repos are ignored")
	return cmd
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
