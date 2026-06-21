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
	"golang.org/x/sync/errgroup"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/serve"
)

type options struct {
	output  string
	timeout time.Duration
}

var version = "v0.9.0" // x-release-please-version

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
		addr         string
		gate         string
		repos        []string
		poll         bool
		pollInterval time.Duration
		pollSource   string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HMAC webhook daemon (default) and/or the opt-in poll trigger",
		RunE: func(cmd *cobra.Command, args []string) error {
			secret := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET"))
			// Webhook is the default; --poll without a secret runs poll-only,
			// bypassing the secret requirement. Webhook (with or without poll)
			// still requires a secret so it never accepts forged payloads.
			if secret == "" && !poll {
				return &CLIError{
					Code:    "serve.secret_required",
					Message: "WEBHOOK_SECRET is required: an empty secret would accept forged webhooks",
					Hint:    "set WEBHOOK_SECRET, or pass --poll to run the poll-only trigger",
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
			if poll {
				switch pollSource {
				case "", "notifications", "pulls":
				default:
					return &CLIError{
						Code:    "serve.poll_source_invalid",
						Message: fmt.Sprintf("unknown --poll-source %q", pollSource),
						Hint:    "use notifications (default) or pulls",
						Exit:    2,
					}
				}
			}

			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))
			reviewTO := 15 * time.Minute
			resolveToken := func() (string, error) { return resolveGitHubToken(""), nil }
			reviewFn := buildServeReviewFn(log, gate)

			pollCfg := func(disp serve.Dispatcher) serve.PollConfig {
				return serve.PollConfig{
					Source:       serve.ParsePollSource(pollSource),
					Repos:        repos,
					Interval:     pollInterval,
					ResolveToken: resolveToken,
					Dispatcher:   disp,
					Logger:       log,
					ReviewTO:     reviewTO,
				}
			}

			// Poll-only: no webhook secret → build the Pool + Poller directly,
			// bypassing serve.New's secret requirement. RunPoll is the sole Drainer.
			if poll && secret == "" {
				pool := serve.NewPool(reviewFn, log)
				poller := serve.NewPoller(pollCfg(pool), serve.NewNotifGetter(token))
				return serve.RunPoll(cmd.Context(), pool, poller)
			}

			// Webhook (+ optional poll). Server.Run is the SOLE Drainer.
			srv, pool, err := serve.New(serve.Config{
				Addr:          addr,
				Secret:        []byte(secret),
				Repos:         repos,
				ResolveToken:  resolveToken,
				Logger:        log,
				ReviewTimeout: reviewTO,
			}, reviewFn)
			if err != nil {
				return &CLIError{Code: "serve.config_invalid", Message: err.Error(), Exit: 2}
			}
			if !poll {
				return srv.Run(cmd.Context(), pool)
			}

			// Webhook + poll share one ctx under an errgroup. The poller dispatches
			// to the SAME pool; it never Drains (Server.Run drains exactly once).
			poller := serve.NewPoller(pollCfg(pool), serve.NewNotifGetter(token))
			g, gctx := errgroup.WithContext(cmd.Context())
			g.Go(func() error { return srv.Run(gctx, pool) })
			// poller.Run only exits on ctx cancel; persistent API failures back off
			// and retry forever (never cancel the group). Surfacing a wedged poller
			// as fatal is a deferred nicety.
			g.Go(func() error { poller.Run(gctx); return nil })
			return g.Wait()
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "Listen address for the webhook server")
	cmd.Flags().StringVar(&gate, "gate", "high", "Publish-severity gate for posted reviews (publish-only; never affects serve liveness)")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Required owner/repo allowlist (comma-separated); webhooks for other repos are ignored")
	cmd.Flags().BoolVar(&poll, "poll", false, "Opt-in poll trigger: periodically ask GitHub for PRs needing review (webhook stays the default)")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 60*time.Second, "Poll interval floor (effective = max(this, X-Poll-Interval))")
	cmd.Flags().StringVar(&pollSource, "poll-source", "notifications", "Poll candidate source: notifications (default) or pulls")
	return cmd
}

// buildServeReviewFn returns the reviewFn shared by webhook + poll dispatch. It
// delegates to the in-process M2 path via ReviewPRForServe (Post:true); all
// errors are redacted. The job runs detached from cmd.Context() so graceful
// drain can finish in-flight reviews; j.Timeout still bounds each job. A non-nil
// return reaches Job.OnDone so the poller leaves a failed head retryable next tick.
func buildServeReviewFn(log *slog.Logger, gate string) func(serve.Job) error {
	return func(j serve.Job) error {
		jobCtx := stdctx.Background()
		if j.Timeout > 0 {
			var cancel stdctx.CancelFunc
			jobCtx, cancel = stdctx.WithTimeout(jobCtx, j.Timeout)
			defer cancel()
		}
		out, err := ReviewPRForServe(jobCtx, PRReviewRequest{
			Ref:   j.Ref,
			Token: j.Token,
			Post:  true,
			// serve inherits both opt-in write-actions OFF: a webhook/poll-driven
			// daemon must not auto-suggest or auto-approve by default.
			Suggest:      false,
			ApproveClean: false,
			Gate:         gate,
			Timeout:      j.Timeout,
		})
		if err != nil {
			log.Error("review failed", "ref", j.Ref, "err", config.RedactString(err.Error()))
			return err
		}
		posted, action := 0, "none"
		if out.PR != nil {
			posted, action = out.PR.PostedInline, out.PR.SummaryAction
		}
		log.Info("review done", "ref", j.Ref, "findings", len(out.Findings), "posted_inline", posted, "summary", action)
		return nil
	}
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
