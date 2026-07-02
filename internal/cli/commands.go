package cli

import (
	stdctx "context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/engine"
	ghub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/serve"
	"github.com/vanducng/miu-cr/internal/store"
)

type options struct {
	output  string
	timeout time.Duration
}

// reviewStoreFactory opens the review store for the REST API, returning the store
// (the serve.ReviewStore subset), a closer, and an error. Injected from the wire
// package (SetReviewStoreFactory) so cli stays below store in the import graph,
// mirroring SetReviewer. Nil when no wiring ran (tests) → serve runs without REST.
var reviewStoreFactory func(ctx stdctx.Context) (serve.ReviewStore, func(), error)

// SetReviewStoreFactory wires the store opener used by `serve` for the opt-in
// REST API. Called once from wire.init before any command runs.
func SetReviewStoreFactory(f func(ctx stdctx.Context) (serve.ReviewStore, func(), error)) {
	reviewStoreFactory = f
}

var hostStoreFactory func(ctx stdctx.Context, cfg config.HostConfig) (store.HostStore, func(), error)

func SetHostStoreFactory(f func(ctx stdctx.Context, cfg config.HostConfig) (store.HostStore, func(), error)) {
	hostStoreFactory = f
}

// historyStoreFactory opens the full review store for the `history` command group
// (list/show/prune). Injected from the wire package so cli stays below store in
// the import graph; nil when no wiring ran (tests inject their own).
var historyStoreFactory func(ctx stdctx.Context) (store.Store, func(), error)

// SetHistoryStoreFactory wires the store opener used by `history`. Called once
// from wire.init before any command runs.
func SetHistoryStoreFactory(f func(ctx stdctx.Context) (store.Store, func(), error)) {
	historyStoreFactory = f
}

var version = "v0.82.0" // x-release-please-version

// Version returns the current miucr version. The literal is bumped by
// release-please; goreleaser ldflags-overrides it with the release tag in
// published binaries.
func Version() string { return version }

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
	root.PersistentFlags().StringVarP(&opts.output, "output", "o", "json", "Output format: json, pretty, or sarif (sarif is review-only)")
	root.PersistentFlags().DurationVar(&opts.timeout, "timeout", 30*time.Second, "Operation timeout")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch opts.output {
		case "", "json":
			outputFormat, prettyOutput = "json", false
		case "pretty":
			outputFormat, prettyOutput = "pretty", true
		case "sarif":
			// SARIF is its own document handled only by review; prettyOutput stays
			// off so every non-review command still emits the JSON envelope.
			outputFormat, prettyOutput = "sarif", false
		default:
			return &CLIError{Code: "output.invalid_format", Message: fmt.Sprintf("unknown output format %q", opts.output), Hint: "use json, pretty, or sarif", Exit: 2}
		}
		return configureDefaultLogger(cmd.ErrOrStderr())
	}
	root.AddCommand(initCommand(opts))
	root.AddCommand(loginCommand(opts))
	root.AddCommand(whoamiCommand(opts))
	root.AddCommand(logoutCommand(opts))
	root.AddCommand(upgradeCommand(opts))
	root.AddCommand(versionCommand())
	root.AddCommand(reviewCommand(opts))
	root.AddCommand(mcpCommand(opts))
	root.AddCommand(serveCommand(opts))
	root.AddCommand(rulesCommand(opts))
	root.AddCommand(historyCommand(opts))
	root.AddCommand(traceCommand(opts))
	root.AddCommand(configCommand(opts))
	root.AddCommand(evalCommand(opts))
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
		host         bool
		dryRunConfig bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HMAC webhook daemon (default) and/or the opt-in poll trigger",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRunConfig && !host {
				return &CLIError{Code: "serve.host_required", Message: "--dry-run-config requires --host", Hint: "use `miucr serve --host --dry-run-config`", Exit: 2}
			}
			if host {
				hostCfg, hostPath, err := loadHostConfigForServe()
				if err != nil {
					return err
				}
				pollSourceChanged := cmd.Flags().Changed("poll-source")
				src, err := serveHostPollSource(hostCfg, pollSource, pollSourceChanged)
				if err != nil {
					return err
				}
				if dryRunConfig {
					safe := config.RedactHostConfig(hostCfg)
					return writeSuccess(cmd.OutOrStdout(), "serve", "serve.host_config", safe, map[string]any{
						"repos":    len(hostCfg.Repos),
						"accounts": len(hostCfg.Github.Accounts),
					})
				}
				if hostStoreFactory == nil {
					return &CLIError{Code: "serve.host_store_unwired", Message: "serve --host store is not wired", Exit: 1}
				}
				log, err := newCLITextLogger(cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				hostStore, closeStore, err := hostStoreFactory(cmd.Context(), hostCfg)
				if err != nil {
					return err
				}
				defer closeStore()
				pollIntervalChanged := cmd.Flags().Changed("poll-interval")
				snapshot, err := buildServeHostSnapshot(cmd.Context(), hostCfg, hostPath, pollInterval, pollIntervalChanged)
				if err != nil {
					return err
				}
				reviewStore, ok := hostStore.(serve.ReviewStore)
				if !ok {
					return &CLIError{Code: "serve.host_store_invalid", Message: "host store does not implement review persistence", Exit: 1}
				}
				workers := hostCfg.Host.Workers
				if workers <= 0 {
					workers = 1
				}
				traceSinkFactory, err := serveTraceSinkFactoryFromEnv(log)
				if err != nil {
					return err
				}
				captureReasoning, err := captureReasoningFromEnv()
				if err != nil {
					return err
				}
				pool := serve.NewPoolWithWorkers(buildServeReviewFn(log, "", reviewStore, traceSinkFactory, captureReasoning), log, workers)
				runner, err := serve.NewHostRunner(serve.HostRunnerConfig{
					Store:           hostStore,
					Repos:           snapshot.Repos,
					TokenSources:    snapshot.TokenSources,
					Reload:          buildServeHostReloader(hostPath, pollInterval, pollIntervalChanged, pollSource, pollSourceChanged),
					Source:          src,
					Interval:        snapshot.Interval,
					Dispatcher:      pool,
					Logger:          log,
					ReviewTO:        snapshot.ReviewTO,
					Prune:           snapshot.Prune,
					JanitorInterval: snapshot.JanitorInterval,
				})
				if err != nil {
					return &CLIError{Code: "serve.host_invalid", Message: config.RedactString(err.Error()), Hint: "check host config repos, accounts, and poll_source", Exit: 2}
				}
				return serve.RunHost(cmd.Context(), pool, runner)
			}
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
			cfg, cfgErr := config.Load()
			if cfgErr != nil {
				// Load() returns Defaults() on error, so we proceed, but log it
				// (redacted) so a malformed config (e.g. [github] mode=app) isn't
				// silently degraded to the PAT default with a confusing downstream error.
				slog.Default().Warn("serve: config load failed; using defaults", "error", config.RedactString(cfgErr.Error()))
			}
			appMode := strings.EqualFold(strings.TrimSpace(cfg.Github.Mode), "app")
			token := resolveGitHubToken("")
			if token == "" && !appMode {
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

			log, err := newCLITextLogger(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			reviewTO := 15 * time.Minute

			tokenSource, err := buildTokenSource(cfg.Github)
			if err != nil {
				return err
			}
			// resolveToken keeps func()(string,error): it captures the daemon ctx
			// (cmd.Context()) and derives a short bounded timeout for the App-token
			// exchange, so a mint isn't tied to one HTTP request's lifetime. In PAT
			// mode the static source returns the PAT unchanged (no network).
			daemonCtx := cmd.Context()
			resolveToken := func() (string, error) {
				tctx, cancel := stdctx.WithTimeout(daemonCtx, 30*time.Second)
				defer cancel()
				return tokenSource.Token(tctx)
			}

			// The REST API is opt-in: enabled ONLY when MIUCR_API_TOKEN (env-only,
			// like WEBHOOK_SECRET, never a flag) is set. When enabled, open the
			// review store once (shared by the POST pending-persist, the GET read,
			// and the worker's final-record upsert).
			apiToken := strings.TrimSpace(os.Getenv("MIUCR_API_TOKEN"))
			var reviewStore serve.ReviewStore
			if apiToken != "" {
				if reviewStoreFactory == nil {
					return &CLIError{Code: "serve.store_unwired", Message: "MIUCR_API_TOKEN is set but the review store is not wired", Exit: 1}
				}
				st, closeStore, err := reviewStoreFactory(cmd.Context())
				if err != nil {
					return err
				}
				defer closeStore()
				reviewStore = st
			}
			traceSinkFactory, err := serveTraceSinkFactoryFromEnv(log)
			if err != nil {
				return err
			}
			captureReasoning, err := captureReasoningFromEnv()
			if err != nil {
				return err
			}
			reviewFn := buildServeReviewFn(log, gate, reviewStore, traceSinkFactory, captureReasoning)

			pollCfg := func(disp serve.Dispatcher) serve.PollConfig {
				return serve.PollConfig{
					Source:       serve.ParsePollSource(pollSource),
					Repos:        repos,
					Interval:     pollInterval,
					ResolveToken: resolveToken,
					Dispatcher:   disp,
					Logger:       log,
					ReviewTO:     reviewTO,
					StalledTO:    defaultReviewStalledTimeout,
				}
			}

			// Poll-only: no webhook secret → build the Pool + Poller directly,
			// bypassing serve.New's secret requirement. RunPoll is the sole Drainer.
			if poll && secret == "" {
				pool := serve.NewPool(reviewFn, log)
				poller := serve.NewPoller(pollCfg(pool), serve.NewNotifGetter(token))
				return serve.RunPoll(cmd.Context(), pool, poller)
			}

			// Webhook (+ optional poll). Server.Run is the SOLE Drainer. The REST
			// /v1 routes are registered only when APIToken + ReviewStore are set.
			srv, pool, err := serve.New(serve.Config{
				Addr:                 addr,
				Secret:               []byte(secret),
				Repos:                repos,
				ResolveToken:         resolveToken,
				Logger:               log,
				ReviewTimeout:        reviewTO,
				ReviewStalledTimeout: defaultReviewStalledTimeout,
				APIToken:             []byte(apiToken),
				ReviewStore:          reviewStore,
			}, reviewFn)
			if err != nil {
				return &CLIError{Code: "config.invalid", Message: err.Error(), Hint: "check the serve configuration (secret, token, repos)", Exit: 2}
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
	cmd.Flags().BoolVar(&host, "host", false, "Run Postgres-backed multi-repo review host mode")
	cmd.Flags().BoolVar(&dryRunConfig, "dry-run-config", false, "Validate host config and print a redacted summary, then exit")
	return cmd
}

func loadHostConfigForServe() (config.HostConfig, string, error) {
	path := strings.TrimSpace(os.Getenv("MIUCR_CONFIG"))
	if path == "" {
		var err error
		path, err = config.HostFilePath()
		if err != nil {
			return config.HostConfig{}, "", err
		}
	}
	cfg, err := config.LoadHost(path)
	return cfg, path, err
}

func hostPollSource(raw string) serve.PollSource {
	if strings.TrimSpace(raw) == "" {
		return serve.SourcePulls
	}
	return serve.ParsePollSource(raw)
}

func serveHostPollSource(cfg config.HostConfig, flagValue string, flagChanged bool) (serve.PollSource, error) {
	src := hostPollSource(cfg.Host.PollSource)
	if flagChanged {
		src = serve.ParsePollSource(flagValue)
	}
	if src != serve.SourcePulls {
		return "", &CLIError{Code: "config.invalid", Message: "serve --host currently supports poll_source pulls only", Hint: "set host.poll_source: pulls or pass --poll-source pulls", Exit: 2}
	}
	return src, nil
}

func buildServeHostSnapshot(ctx stdctx.Context, cfg config.HostConfig, path string, pollInterval time.Duration, pollIntervalChanged bool) (serve.HostReload, error) {
	tokenSources, err := buildHostGitHubTokenSources(ctx, cfg)
	if err != nil {
		return serve.HostReload{}, err
	}
	hostRepos, reviewTO, err := buildServeHostRepos(ctx, cfg, path)
	if err != nil {
		return serve.HostReload{}, err
	}
	interval := durationOrDefault(cfg.Host.PollInterval, time.Minute)
	if pollIntervalChanged {
		interval = pollInterval
	}
	return serve.HostReload{
		Repos:           hostRepos,
		TokenSources:    tokenSources,
		Interval:        interval,
		ReviewTO:        reviewTO,
		Prune:           hostPruneConfig(cfg.Host),
		JanitorInterval: durationOrDefault(cfg.Host.Retention.JanitorInterval, 15*time.Minute),
	}, nil
}

type serveHostReloader struct {
	path                string
	pollInterval        time.Duration
	pollIntervalChanged bool
	pollSource          string
	pollSourceChanged   bool
	mu                  sync.Mutex
	fingerprint         string
}

func buildServeHostReloader(path string, pollInterval time.Duration, pollIntervalChanged bool, pollSource string, pollSourceChanged bool) serve.HostReloadFunc {
	r := &serveHostReloader{path: path, pollInterval: pollInterval, pollIntervalChanged: pollIntervalChanged, pollSource: pollSource, pollSourceChanged: pollSourceChanged}
	return r.reload
}

func (r *serveHostReloader) reload(ctx stdctx.Context) (serve.HostReload, error) {
	cfg, err := config.LoadHost(r.path)
	if err != nil {
		return serve.HostReload{}, err
	}
	if _, err := serveHostPollSource(cfg, r.pollSource, r.pollSourceChanged); err != nil {
		return serve.HostReload{}, err
	}
	fingerprint, cacheable, err := hostReloadFingerprint(cfg, r.path)
	if err != nil {
		return serve.HostReload{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cacheable && fingerprint == r.fingerprint {
		return serve.HostReload{}, nil
	}
	next, err := buildServeHostSnapshot(ctx, cfg, r.path, r.pollInterval, r.pollIntervalChanged)
	if err != nil {
		return serve.HostReload{}, err
	}
	if cacheable {
		r.fingerprint = fingerprint
	} else {
		r.fingerprint = ""
	}
	return next, nil
}

type hostReloadTextFingerprint struct {
	Ref  string `json:"ref"`
	Hash string `json:"hash"`
}

type hostReloadFileFingerprint struct {
	Ref     string `json:"ref"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mod_time"`
}

func hostReloadFingerprint(cfg config.HostConfig, path string) (string, bool, error) {
	baseDir := filepath.Dir(path)
	prompts, err := hostPromptFingerprints(baseDir, cfg)
	if err != nil {
		return "", false, err
	}
	rules, err := hostRuleFingerprints(baseDir, cfg)
	if err != nil {
		return "", false, err
	}
	keyFiles, cacheable, err := hostKeyFileFingerprints(cfg)
	if err != nil {
		return "", false, err
	}
	return serve.HashJSON(struct {
		Config   config.HostConfig           `json:"config"`
		Prompts  []hostReloadTextFingerprint `json:"prompts"`
		Rules    []hostReloadTextFingerprint `json:"rules"`
		KeyFiles []hostReloadFileFingerprint `json:"key_files"`
	}{cfg, prompts, rules, keyFiles}), cacheable, nil
}

func hostPromptFingerprints(baseDir string, cfg config.HostConfig) ([]hostReloadTextFingerprint, error) {
	var out []hostReloadTextFingerprint
	if strings.TrimSpace(cfg.Agent.SystemPromptFile) != "" {
		text, err := hostPrompt(baseDir, cfg.Agent)
		if err != nil {
			return nil, err
		}
		out = append(out, hostReloadTextFingerprint{Ref: cfg.Agent.SystemPromptFile, Hash: hashString(text)})
	}
	for _, repo := range cfg.Repos {
		if strings.TrimSpace(repo.Agent.SystemPromptFile) == "" {
			continue
		}
		text, err := hostPrompt(baseDir, repo.Agent)
		if err != nil {
			return nil, err
		}
		out = append(out, hostReloadTextFingerprint{Ref: repo.Slug + ":" + repo.Agent.SystemPromptFile, Hash: hashString(text)})
	}
	return out, nil
}

func hostRuleFingerprints(baseDir string, cfg config.HostConfig) ([]hostReloadTextFingerprint, error) {
	var out []hostReloadTextFingerprint
	for _, repo := range cfg.Repos {
		_, rulesHash, err := hostRuleText(baseDir, repo.Rules)
		if err != nil {
			return nil, err
		}
		out = append(out, hostReloadTextFingerprint{Ref: repo.Slug, Hash: rulesHash})
	}
	return out, nil
}

func hostKeyFileFingerprints(cfg config.HostConfig) ([]hostReloadFileFingerprint, bool, error) {
	names := make([]string, 0, len(cfg.Github.Accounts))
	for name := range cfg.Github.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]hostReloadFileFingerprint, 0, len(names))
	cacheable := true
	for _, name := range names {
		acct := cfg.Github.Accounts[name]
		if acct.Mode != "app" {
			continue
		}
		path := firstHostValue(acct.PrivateKeyPath, os.Getenv(acct.PrivateKeyPathEnv))
		if path == "" {
			if len(acct.PrivateKeyCommand) > 0 {
				cacheable = false
			}
			continue
		}
		fp, err := hostFileFingerprint(name, path)
		if err != nil {
			return nil, false, err
		}
		out = append(out, fp)
	}
	return out, cacheable, nil
}

func hostFileFingerprint(ref, path string) (hostReloadFileFingerprint, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return hostReloadFileFingerprint{}, &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix host watched file " + ref, Exit: 2}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return hostReloadFileFingerprint{}, &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix host watched file " + ref, Exit: 2}
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return hostReloadFileFingerprint{}, &CLIError{Code: "config.invalid", Message: "host watched file must be a regular file: " + ref, Hint: "use a regular mounted file", Exit: 2}
	}
	return hostReloadFileFingerprint{Ref: ref, Path: path, Size: info.Size(), Mode: info.Mode().String(), ModTime: info.ModTime().UnixNano()}, nil
}

func buildServeHostRepos(ctx stdctx.Context, cfg config.HostConfig, path string) ([]serve.HostRepoConfig, time.Duration, error) {
	baseDir := filepath.Dir(path)
	provider, err := hostProvider(cfg)
	if err != nil {
		return nil, 0, err
	}
	providerName := hostProviderName(cfg)
	baseReview := mergeHostReview(config.HostReview{
		Gate:                 "high",
		FilterMode:           "diff_context",
		Timeout:              "900s",
		StalledTimeout:       "5m",
		ProviderRetry:        config.DefaultProviderRetry(),
		Post:                 boolSetting(true),
		Suggest:              boolSetting(false),
		PatchRepair:          boolSetting(false),
		ThreadResolutionSync: config.ThreadResolutionSyncConfig{Mode: "off", Interval: "5m"},
		Approval:             config.ApprovalPolicy{Mode: "off"},
		Force:                boolSetting(false),
		Conversation:         boolSetting(false),
		DeepContext:          boolSetting(false),
		Expand:               intSetting(5),
		ContextHops:          intSetting(0),
	}, cfg.Review)
	hostReview := mergeHostReview(baseReview, cfg.Host.Review)
	reviewTO := durationOrDefault(hostReview.Timeout, 15*time.Minute)
	if provider.Kind != config.KindOpenAI && provider.Kind != config.KindAnthropic && provider.Kind != "" {
		return nil, 0, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("unknown host provider kind %q", provider.Kind), Hint: "kind must be anthropic or openai", Exit: 2}
	}
	providerSecret, err := hostProviderSecret(ctx, provider)
	if err != nil {
		return nil, 0, err
	}
	out := make([]serve.HostRepoConfig, 0, len(cfg.Repos))
	hostEnabled := cfg.Host.Enabled == nil || *cfg.Host.Enabled
	hostPoll := cfg.Host.Poll == nil || *cfg.Host.Poll
	for _, repo := range cfg.Repos {
		enabled := hostEnabled && (repo.Enabled == nil || *repo.Enabled)
		poll := hostPoll && (repo.Poll == nil || *repo.Poll)
		review := mergeHostReview(hostReview, repo.Review)
		opts, err := hostReviewOptions(providerName, provider, providerSecret, review)
		if err != nil {
			return nil, 0, err
		}
		threadResolutionSync, err := hostThreadResolutionSync(review.ThreadResolutionSync)
		if err != nil {
			return nil, 0, err
		}
		operatorPrompt, promptHash, rulesHash, err := hostOperatorPrompt(baseDir, cfg.Agent, repo.Agent, repo.Rules)
		if err != nil {
			return nil, 0, err
		}
		opts.OperatorPrompt = operatorPrompt
		out = append(out, serve.HostRepoConfig{
			Name:                 repo.Name,
			Owner:                repo.Owner,
			Repo:                 repo.Repo,
			Slug:                 repo.Slug,
			GitURL:               repo.GitURL,
			DefaultBranch:        repo.DefaultBranch,
			GithubAccount:        repo.GithubAccount,
			Enabled:              enabled,
			Poll:                 poll,
			ConfigHash:           serve.HashJSON(repo),
			PolicyHash:           serve.HashJSON(hostReviewAnalysisShape(review)),
			PromptHash:           promptHash,
			RulesHash:            rulesHash,
			ReviewTimeout:        durationOrDefault(review.Timeout, reviewTO),
			ThreadResolutionSync: threadResolutionSync,
			Review:               opts,
			PRFilter:             review.PRFilter,
		})
	}
	return out, reviewTO, nil
}

type hostReviewAnalysisFields struct {
	Gate           string
	FilterMode     string
	MinSeverity    string
	PromptFormat   string
	Timeout        string
	StalledTimeout string
	ProviderRetry  config.ProviderRetry
	Expand         *int
	TokenBudget    *int
	ContextHops    *int
	Mode           string
	Thinking       string
	DeepContext    *bool
	Conversation   *bool
	Force          *bool
	PatchRepair    *bool
	Tools          config.ReviewTools
	Subagents      config.ReviewSubagents
}

func hostReviewAnalysisShape(review config.HostReview) any {
	return hostReviewAnalysisFields{
		Gate:           review.Gate,
		FilterMode:     review.FilterMode,
		MinSeverity:    review.MinSeverity,
		Timeout:        review.Timeout,
		StalledTimeout: review.StalledTimeout,
		ProviderRetry:  review.ProviderRetry,
		Expand:         review.Expand,
		TokenBudget:    review.TokenBudget,
		ContextHops:    review.ContextHops,
		Mode:           review.Mode,
		Thinking:       review.Thinking,
		DeepContext:    review.DeepContext,
		Conversation:   review.Conversation,
		Force:          review.Force,
		PatchRepair:    review.PatchRepair,
		Tools:          review.Tools,
		Subagents:      review.Subagents,
		PromptFormat:   review.PromptFormat,
	}
}

func hostReviewOptions(providerName string, provider config.HostProvider, secret string, review config.HostReview) (serve.JobReviewOptions, error) {
	opts := serve.JobReviewOptions{
		Post:                boolValue(review.Post),
		Suggest:             boolValue(review.Suggest),
		PatchRepair:         boolValue(review.PatchRepair),
		Approval:            review.Approval,
		Force:               boolValue(review.Force),
		Conversation:        boolValue(review.Conversation),
		Gate:                review.Gate,
		FilterMode:          review.FilterMode,
		MinSeverity:         review.MinSeverity,
		Format:              review.Format,
		Thinking:            review.Thinking,
		SuppressWalkthrough: !review.CodeSummary.WantWalkthrough(),
		FileChangeSummary:   review.CodeSummary.WantFileChangeSummary(),
		PromptFormat:        review.PromptFormat,
		Mode:                review.Mode,
		BaseURL:             provider.BaseURL,
		Model:               provider.Model,
		StalledTimeout:      durationOrDefaultAllowZero(review.StalledTimeout, defaultReviewStalledTimeout),
		ProviderRetry:       review.ProviderRetry,
		DeepContext:         boolValue(review.DeepContext),
		Subagents:           review.Subagents,
		Tools:               review.Tools,
		SymbolContext:       review.Tools.SymbolContext,
		Quota:               provider.Quota,
		QuotaProvider:       providerName,
	}
	if review.Expand != nil {
		opts.ExpandWindow = *review.Expand
	}
	if review.ContextHops != nil {
		opts.ContextHops = *review.ContextHops
	}
	if review.TokenBudget != nil {
		opts.TokenBudget = *review.TokenBudget
	}
	switch provider.Kind {
	case config.KindOpenAI:
		opts.Provider = string(config.KindOpenAI)
		opts.APIKey = secret
	case config.KindAnthropic, "":
		opts.Provider = string(config.KindAnthropic)
		if strings.EqualFold(provider.Auth, "api_key") {
			opts.APIKey = secret
		} else {
			opts.AuthToken = secret
		}
	default:
		return opts, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("unknown host provider kind %q", provider.Kind), Hint: "kind must be anthropic or openai", Exit: 2}
	}
	return opts, nil
}

func hostProviderSecret(ctx stdctx.Context, provider config.HostProvider) (string, error) {
	if strings.TrimSpace(provider.AuthToken) != "" {
		return strings.TrimSpace(provider.AuthToken), nil
	}
	return hostSecret(ctx, provider.AuthEnv, "", provider.AuthCommand)
}

// hostProviderName resolves the configured default provider instance name (the
// quota counter key), falling back to the built-in default. Single source so the
// serve dispatch and hostProvider agree on the name.
func hostProviderName(cfg config.HostConfig) string {
	name := strings.TrimSpace(cfg.DefaultProvider)
	if name == "" {
		name = config.Defaults().DefaultProvider
	}
	return name
}

func hostProvider(cfg config.HostConfig) (config.HostProvider, error) {
	defaults := config.Defaults()
	name := hostProviderName(cfg)
	if p, ok := cfg.Providers[name]; ok {
		return p, nil
	}
	if p, ok := defaults.Providers[name]; ok {
		return config.HostProvider{Kind: p.Kind, BaseURL: p.BaseURL, Model: p.Model, AuthEnv: p.AuthEnv, AuthCommand: p.AuthCommand, Auth: p.Auth, Quota: p.Quota}, nil
	}
	return config.HostProvider{}, &CLIError{Code: "agent.unknown_provider", Message: fmt.Sprintf("unknown host provider %q", name), Hint: "define it under providers in the host YAML", Exit: 2}
}

func hostOperatorPrompt(baseDir string, global, repo config.HostAgent, ruleRefs []string) (string, string, string, error) {
	prompt, err := hostPrompt(baseDir, global)
	if err != nil {
		return "", "", "", err
	}
	if repo.SystemPrompt != "" || repo.SystemPromptFile != "" {
		prompt, err = hostPrompt(baseDir, repo)
		if err != nil {
			return "", "", "", err
		}
	}
	rulesText, rulesHash, err := hostRuleText(baseDir, ruleRefs)
	if err != nil {
		return "", "", "", err
	}
	operator := strings.TrimSpace(prompt)
	if strings.TrimSpace(rulesText) != "" {
		if operator != "" {
			operator += "\n\n"
		}
		operator += "Trusted host rules:\n" + rulesText
	}
	return operator, hashString(operator), rulesHash, nil
}

func hostPrompt(baseDir string, agent config.HostAgent) (string, error) {
	if strings.TrimSpace(agent.SystemPrompt) != "" {
		return strings.TrimSpace(agent.SystemPrompt), nil
	}
	if strings.TrimSpace(agent.SystemPromptFile) == "" {
		return "", nil
	}
	return readHostTextRef(baseDir, agent.SystemPromptFile)
}

func hostRuleText(baseDir string, refs []string) (string, string, error) {
	var parts []string
	for _, ref := range refs {
		texts, err := readHostRuleRef(baseDir, ref)
		if err != nil {
			return "", "", err
		}
		for _, text := range texts {
			if strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
	}
	joined := strings.Join(parts, "\n\n")
	return joined, hashString(joined), nil
}

func readHostRuleRef(baseDir, ref string) ([]string, error) {
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	f, info, err := openHostPath(path, ref)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !info.IsDir() {
		text, err := readHostTextFile(f, info, ref)
		if err != nil {
			return nil, err
		}
		return []string{text}, nil
	}

	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix host rule directory " + ref, Exit: 2}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []string
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".md" {
			continue
		}
		childRef := filepath.Join(ref, ent.Name())
		child, childInfo, err := openHostPath(filepath.Join(path, ent.Name()), childRef)
		if err != nil {
			return nil, err
		}
		text, err := readHostTextFile(child, childInfo, childRef)
		_ = child.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}

func readHostTextRef(baseDir, ref string) (string, error) {
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	f, info, err := openHostPath(path, ref)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if info.IsDir() {
		return "", &CLIError{Code: "config.invalid", Message: "host prompt path must be a file: " + ref, Hint: "set system_prompt_file to a regular markdown file", Exit: 2}
	}
	return readHostTextFile(f, info, ref)
}

func openHostPath(path, ref string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		if isSymlinkLoopErr(err) {
			return nil, nil, &CLIError{Code: "config.invalid", Message: "host prompt/rule path is a symlink: " + ref, Hint: "use a regular mounted file", Exit: 2}
		}
		return nil, nil, &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix host prompt/rule path " + ref, Exit: 2}
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix host prompt/rule path " + ref, Exit: 2}
	}
	return f, info, nil
}

func readHostTextFile(f *os.File, info os.FileInfo, ref string) (string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		return "", &CLIError{Code: "config.invalid", Message: "host prompt/rule path is a symlink: " + ref, Hint: "use a regular mounted file", Exit: 2}
	}
	if info.IsDir() {
		return "", &CLIError{Code: "config.invalid", Message: "host prompt/rule path must be a file: " + ref, Hint: "use a regular mounted file", Exit: 2}
	}
	if info.Size() > 64*1024 {
		return "", &CLIError{Code: "config.invalid", Message: "host prompt/rule file is too large: " + ref, Hint: "keep each host prompt/rule file under 64KiB", Exit: 2}
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return "", &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix host prompt/rule path " + ref, Exit: 2}
	}
	if len(data) > 64*1024 {
		return "", &CLIError{Code: "config.invalid", Message: "host prompt/rule file is too large: " + ref, Hint: "keep each host prompt/rule file under 64KiB", Exit: 2}
	}
	return string(data), nil
}

func buildHostGitHubTokenSources(ctx stdctx.Context, cfg config.HostConfig) (map[string]serve.HostTokenSource, error) {
	out := make(map[string]serve.HostTokenSource, len(cfg.Github.Accounts))
	for name, acct := range cfg.Github.Accounts {
		switch acct.Mode {
		case "pat":
			out[name] = hostPATSource{env: acct.AuthEnv, file: acct.AuthFile, command: acct.AuthCommand}
		case "app":
			src, err := buildHostAppTokenSource(ctx, acct)
			if err != nil {
				return nil, err
			}
			out[name] = src
		default:
			return nil, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("unknown GitHub account mode %q", acct.Mode), Hint: "use pat or app", Exit: 2}
		}
	}
	return out, nil
}

type hostPATSource struct {
	env     string
	file    string
	command []string
}

func (s hostPATSource) Token(ctx stdctx.Context) (string, error) {
	return hostSecret(ctx, s.env, s.file, s.command)
}

func buildHostAppTokenSource(ctx stdctx.Context, acct config.HostGithubAccount) (serve.HostTokenSource, error) {
	appID := firstHostValue(acct.AppID, os.Getenv(acct.AppIDEnv))
	if appID == "" {
		return nil, &CLIError{Code: "config.invalid", Message: "github app app_id is required", Hint: "fix github.accounts.*.app_id", Exit: 2}
	}
	install := firstHostValue(acct.InstallationID, os.Getenv(acct.InstallationIDEnv))
	installID, err := strconv.ParseInt(strings.TrimSpace(install), 10, 64)
	if err != nil || installID <= 0 {
		return nil, &CLIError{Code: "config.invalid", Message: "github app installation_id must be a positive integer", Hint: "fix github.accounts.*.installation_id", Exit: 2}
	}
	keyPath := firstHostValue(acct.PrivateKeyPath, os.Getenv(acct.PrivateKeyPathEnv))
	if keyPath != "" {
		key, err := ghub.ReadPrivateKeyFile(keyPath)
		if err != nil {
			return nil, &CLIError{Code: "serve.app_key_unreadable", Message: config.RedactString(err.Error()), Hint: "private_key_path must point to a readable RSA PEM", Exit: 2}
		}
		return ghub.NewAppTokenSource(strings.TrimSpace(appID), installID, key, ghub.NewAppExchanger(), nil), nil
	}
	secret, err := runHostBlobCommand(ctx, acct.PrivateKeyCommand)
	if err != nil {
		return nil, err
	}
	keyPEM := []byte(secret)
	key, err := ghub.ParsePrivateKeyPEM(keyPEM)
	for i := range keyPEM {
		keyPEM[i] = 0
	}
	if err != nil {
		return nil, &CLIError{Code: "serve.app_key_unreadable", Message: config.RedactString(err.Error()), Hint: "private_key_command must print a readable RSA PEM", Exit: 2}
	}
	return ghub.NewAppTokenSource(strings.TrimSpace(appID), installID, key, ghub.NewAppExchanger(), nil), nil
}

func hostSecret(ctx stdctx.Context, envName, file string, command []string) (string, error) {
	if strings.TrimSpace(envName) != "" {
		if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
			return v, nil
		}
		return "", &CLIError{Code: "config.invalid", Message: "configured env secret is empty: " + envName, Hint: "export " + envName, Exit: 2}
	}
	if strings.TrimSpace(file) != "" {
		return readHostSecretFile(file)
	}
	if len(command) > 0 {
		return runHostSecretCommand(ctx, command)
	}
	return "", nil
}

func readHostSecretFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		if isSymlinkLoopErr(err) {
			return "", &CLIError{Code: "config.invalid", Message: "configured secret file is a symlink", Hint: "use a regular mounted secret file", Exit: 2}
		}
		return "", &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix configured secret file path", Exit: 2}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix configured secret file path", Exit: 2}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", &CLIError{Code: "config.invalid", Message: "configured secret file is a symlink", Hint: "use a regular mounted secret file", Exit: 2}
	}
	if info.IsDir() {
		return "", &CLIError{Code: "config.invalid", Message: "configured secret file path must be a file", Hint: "use a regular mounted secret file", Exit: 2}
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return "", &CLIError{Code: "config.invalid", Message: config.RedactString(err.Error()), Hint: "fix configured secret file path", Exit: 2}
	}
	if len(data) > 64*1024 {
		return "", &CLIError{Code: "config.invalid", Message: "configured secret file is too large", Hint: "keep configured secret files under 64KiB", Exit: 2}
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", &CLIError{Code: "config.invalid", Message: "configured secret file is empty", Hint: "fix configured secret file path", Exit: 2}
	}
	return secret, nil
}

func runHostSecretCommand(ctx stdctx.Context, argv []string) (string, error) {
	secret, err := runHostCommandOutput(ctx, argv)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(secret, "\r\n") {
		return "", &CLIError{Code: "agent.auth_command_failed", Message: "auth_command printed multiple lines", Hint: "ensure the command prints exactly one credential line", Exit: 1}
	}
	return secret, nil
}

func runHostBlobCommand(ctx stdctx.Context, argv []string) (string, error) {
	return runHostCommandOutput(ctx, argv)
}

func runHostCommandOutput(ctx stdctx.Context, argv []string) (string, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return "", &CLIError{Code: "config.invalid", Message: "auth_command must be a non-empty argv array", Hint: "set auth_command as a YAML list", Exit: 2}
	}
	args := append([]string(nil), argv...)
	args[0] = strings.TrimSpace(args[0])
	cctx, cancel := stdctx.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, args[0], args[1:]...)
	var stdout secretBuffer
	stdout.limit = 64 * 1024
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return "", &CLIError{Code: "agent.auth_command_failed", Message: "auth_command binary not found: " + args[0], Hint: "verify the command path in auth_command", Exit: 1, Cause: err}
		}
		return "", &CLIError{Code: "agent.auth_command_failed", Message: "auth_command failed", Hint: "run the configured command directly; stderr is omitted because it may contain secrets", Exit: 1, Cause: err}
	}
	secret := strings.TrimSpace(stdout.String())
	if secret == "" {
		return "", &CLIError{Code: "agent.auth_command_failed", Message: "auth_command printed no credential", Hint: "ensure the command prints the token to stdout", Exit: 1}
	}
	return secret, nil
}

type secretBuffer struct {
	buf   strings.Builder
	limit int
}

func (b *secretBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit > 0 && b.buf.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("command output exceeded %d byte limit", b.limit)
	}
	if len(p) > 0 {
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

func (b *secretBuffer) String() string { return b.buf.String() }

func mergeHostReview(base, over config.HostReview) config.HostReview {
	out := base
	if over.Gate != "" {
		out.Gate = over.Gate
	}
	if over.FilterMode != "" {
		out.FilterMode = over.FilterMode
	}
	if over.MinSeverity != "" {
		out.MinSeverity = over.MinSeverity
	}
	if over.Format != "" {
		out.Format = over.Format
	}
	if over.Thinking != "" {
		out.Thinking = over.Thinking
	}
	if over.CodeSummary.Walkthrough != nil {
		out.CodeSummary.Walkthrough = over.CodeSummary.Walkthrough
	}
	if over.CodeSummary.FileChangeSummary != nil {
		out.CodeSummary.FileChangeSummary = over.CodeSummary.FileChangeSummary
	}
	if over.PromptFormat != "" {
		out.PromptFormat = over.PromptFormat
	}
	if over.Timeout != "" {
		out.Timeout = over.Timeout
	}
	if over.StalledTimeout != "" {
		out.StalledTimeout = over.StalledTimeout
	}
	out.ProviderRetry = config.MergeProviderRetry(base.ProviderRetry, over.ProviderRetry)
	if over.Expand != nil {
		out.Expand = over.Expand
	}
	if over.TokenBudget != nil {
		out.TokenBudget = over.TokenBudget
	}
	if over.ContextHops != nil {
		out.ContextHops = over.ContextHops
	}
	if over.Mode != "" {
		out.Mode = over.Mode
	}
	if over.DeepContext != nil {
		out.DeepContext = over.DeepContext
	}
	if over.Conversation != nil {
		out.Conversation = over.Conversation
	}
	if over.Post != nil {
		out.Post = over.Post
	}
	if over.Force != nil {
		out.Force = over.Force
	}
	if over.Suggest != nil {
		out.Suggest = over.Suggest
	}
	if over.PatchRepair != nil {
		out.PatchRepair = over.PatchRepair
	}
	out.Tools = config.MergeReviewTools(base.Tools, over.Tools)
	out.ThreadResolutionSync = config.MergeThreadResolutionSyncConfig(base.ThreadResolutionSync, over.ThreadResolutionSync)
	out.Approval = config.MergeApprovalPolicy(base.Approval, over.Approval)
	out.Subagents = config.MergeReviewSubagents(base.Subagents, over.Subagents)
	out.PRFilter = config.MergePRFilter(base.PRFilter, over.PRFilter)
	return out
}

func hostThreadResolutionSync(raw config.ThreadResolutionSyncConfig) (serve.HostThreadResolutionSync, error) {
	mode := strings.ToLower(strings.TrimSpace(raw.Mode))
	if mode == "" {
		mode = "off"
	}
	switch mode {
	case "off":
	case "poll":
	default:
		return serve.HostThreadResolutionSync{}, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("unknown thread_resolution_sync mode %q", raw.Mode), Hint: "use mode: off or mode: poll", Exit: 2}
	}
	interval := 5 * time.Minute
	if strings.TrimSpace(raw.Interval) != "" {
		d, err := time.ParseDuration(raw.Interval)
		if err != nil || d <= 0 {
			return serve.HostThreadResolutionSync{}, &CLIError{Code: "config.invalid", Message: fmt.Sprintf("invalid thread_resolution_sync interval %q", raw.Interval), Hint: "use a positive Go duration like 5m", Exit: 2}
		}
		interval = d
	}
	return serve.HostThreadResolutionSync{Mode: mode, Interval: interval}, nil
}

func boolValue(v *bool) bool { return v != nil && *v }

func boolSetting(v bool) *bool { return &v }

func intSetting(v int) *int { return &v }

func durationOrDefault(raw string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return fallback
}

func durationOrDefaultAllowZero(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}

func hostPruneConfig(h config.HostRuntime) serve.HostPruneConfig {
	r := h.Retention
	dbTTL := durationOrDefault(r.DBTTL, 90*24*time.Hour)
	return serve.HostPruneConfig{
		ClosedSessionTTL:     durationOrDefault(r.ClosedWorkspaceTTL, 24*time.Hour),
		CompletedJobTTL:      dbTTL,
		FinishedAttemptTTL:   dbTTL,
		InactiveWorkspaceTTL: durationOrDefault(r.WorkspaceTTL, 168*time.Hour),
		PollCursorTTL:        dbTTL,
	}
}

func firstHostValue(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func hashString(s string) string {
	return serve.HashJSON(struct {
		Text string `json:"text"`
	}{s})
}

// buildTokenSource builds the GitHub TokenSource from [github]. Default (mode=pat
// or unset) → staticTokenSource carrying the resolved PAT (or "" anonymous), the
// pre-M8 behavior byte-for-byte. mode=app → appTokenSource (requires app_id, a
// numeric installation_id, and a readable private_key_path); the key is read,
// parsed, and zeroed inside the github package.
func buildTokenSource(g config.Github) (ghub.TokenSource, error) {
	if !strings.EqualFold(strings.TrimSpace(g.Mode), "app") {
		return ghub.NewStaticTokenSource(resolveGitHubToken("")), nil
	}
	if strings.TrimSpace(g.AppID) == "" || strings.TrimSpace(g.PrivateKeyPath) == "" {
		return nil, &CLIError{
			Code:    "serve.app_config_required",
			Message: "[github] mode=app requires app_id and private_key_path",
			Hint:    "set app_id, installation_id, and private_key_path under [github] in config.toml",
			Exit:    2,
		}
	}
	installID, err := strconv.ParseInt(strings.TrimSpace(g.InstallationID), 10, 64)
	if err != nil || installID <= 0 {
		return nil, &CLIError{
			Code:    "serve.app_installation_invalid",
			Message: fmt.Sprintf("[github] installation_id must be a positive integer, got %q", g.InstallationID),
			Hint:    "use the numeric installation id from the App's installation URL",
			Exit:    2,
		}
	}
	key, err := ghub.ReadPrivateKeyFile(strings.TrimSpace(g.PrivateKeyPath))
	if err != nil {
		return nil, &CLIError{
			Code:    "serve.app_key_unreadable",
			Message: config.RedactString(err.Error()),
			Hint:    "private_key_path must point to a readable RSA PEM (PKCS#1 or PKCS#8)",
			Exit:    2,
		}
	}
	return ghub.NewAppTokenSource(strings.TrimSpace(g.AppID), installID, key, ghub.NewAppExchanger(), nil), nil
}

// buildServeReviewFn returns the reviewFn shared by webhook + poll dispatch. It
// delegates to the in-process M2 path via ReviewPRForServe (Post:true); all
// errors are redacted. The job runs detached from cmd.Context() so graceful
// drain can finish in-flight reviews; j.Timeout still bounds each job. A non-nil
// return reaches Job.OnDone so the poller leaves a failed head retryable next tick.
//
// When j.ReviewID is set (REST-initiated) and st is non-nil, reviewFn persists the
// FINAL record under that id: done (with the returned outcome's findings/stats/
// HeadSHA) on success, failed on error. The findings/stats/HeadSHA live in the
// RETURNED cli.ReviewOutcome (not in Job.OnDone(error)), so the upsert rides here,
// inside reviewFn, not in OnDone. The webhook/poll paths leave ReviewID empty and
// skip the upsert (byte-for-byte unchanged).
func buildServeReviewFn(log *slog.Logger, gate string, st serve.ReviewStore, traceSinkFactory func(*slog.Logger) func(step string, payload any), captureReasoning bool) func(serve.Job) error {
	return func(j serve.Job) error {
		var traceSink func(step string, payload any)
		if traceSinkFactory != nil {
			traceSink = traceSinkFactory(log.With(serveJobLogAttrs(j)...))
		}
		review := serve.JobReviewOptions{Post: true, Gate: gate}
		if j.Review != nil {
			review = *j.Review
			if review.Gate == "" {
				review.Gate = gate
			}
		}
		timeout := j.Timeout
		if timeout <= 0 {
			timeout = 15 * time.Minute
		}
		stalledTimeout := j.StalledTimeout
		if j.Review != nil {
			stalledTimeout = review.StalledTimeout
		}
		parentCtx := j.Context
		if parentCtx == nil {
			parentCtx = stdctx.Background()
		}
		jobCtx, cancel := stdctx.WithTimeout(parentCtx, timeout)
		defer cancel()
		progress := func(stage string) {
			log.Debug("review progress", serveJobLogAttrs(j, "stage", config.RedactString(stage))...)
		}
		jobCtx, progress, stopWatchdog := withReviewProgressWatchdog(jobCtx, stalledTimeout, progress)
		defer stopWatchdog()
		out, err := ReviewPRForServe(jobCtx, PRReviewRequest{
			Ref:                 j.Ref,
			Token:               j.Token,
			Post:                review.Post,
			Suggest:             review.Suggest,
			PatchRepair:         review.PatchRepair,
			Approval:            review.Approval,
			Gate:                review.Gate,
			Provider:            review.Provider,
			APIKey:              review.APIKey,
			BaseURL:             review.BaseURL,
			AuthToken:           review.AuthToken,
			Model:               review.Model,
			Quota:               review.Quota,
			QuotaProvider:       review.QuotaProvider,
			Timeout:             timeout,
			ExpandWindow:        review.ExpandWindow,
			TokenBudget:         review.TokenBudget,
			DeepContext:         review.DeepContext,
			ContextHops:         review.ContextHops,
			Subagents:           review.Subagents,
			ProviderRetry:       review.ProviderRetry,
			Tools:               review.Tools,
			SymbolContext:       review.SymbolContext,
			FilterMode:          review.FilterMode,
			MinSeverity:         review.MinSeverity,
			Format:              review.Format,
			Thinking:            review.Thinking,
			SuppressWalkthrough: review.SuppressWalkthrough,
			FileChangeSummary:   review.FileChangeSummary,
			PromptFormat:        review.PromptFormat,
			OperatorPrompt:      review.OperatorPrompt,
			Conversation:        review.Conversation,
			Mode:                review.Mode,
			Force:               review.Force,
			Progress:            progress,
			TraceSink:           traceSink,
			CaptureReasoning:    captureReasoning,
		})
		if err != nil {
			err = classifyReviewErr(err, timeout, jobCtx)
			if errors.Is(err, stdctx.Canceled) {
				log.Info("review canceled", serveJobLogAttrs(j, "err", config.RedactString(err.Error()))...)
				persistFinalReview(log, st, j.ReviewID, "failed", ReviewOutcome{})
				return err
			}
			// A quota-exhausted provider is a terminal skip for THIS job, not a
			// failure: returning err would retry it every poll until the window
			// resets, spamming the provider. Mark done + log; a later push or
			// comment re-triggers a fresh job that re-checks the quota.
			var ce *CLIError
			if errors.As(err, &ce) && ce.Code == "quota.exceeded" {
				log.Warn("review skipped: provider quota exhausted", serveJobLogAttrs(j, "err", config.RedactString(err.Error()))...)
				persistFinalReview(log, st, j.ReviewID, "done", ReviewOutcome{})
				return nil
			}
			log.Error("review failed", serveJobLogAttrs(j, "err", config.RedactString(err.Error()))...)
			persistFinalReview(log, st, j.ReviewID, "failed", ReviewOutcome{})
			return err
		}
		posted, action := 0, "none"
		if out.PR != nil {
			posted, action = out.PR.PostedInline, out.PR.SummaryAction
		}
		log.Info("review done", serveJobLogAttrs(j, "review_id", out.ReviewID, "findings", len(out.Findings), "posted_inline", posted, "summary", action)...)
		persistFinalReview(log, st, j.ReviewID, "done", out)
		return nil
	}
}

func serveJobLogAttrs(j serve.Job, attrs ...any) []any {
	out := []any{"ref", j.Ref}
	if j.Title != "" {
		out = append(out, "pr_title", config.RedactString(j.Title))
	}
	if j.HostJobID != 0 {
		out = append(out, "job_id", j.HostJobID)
	}
	if j.HostAttemptID != 0 {
		out = append(out, "attempt_id", j.HostAttemptID)
	}
	if j.HostAttempt != 0 {
		out = append(out, "attempt", j.HostAttempt)
	}
	if j.HeadSHA != "" {
		out = append(out, "head_sha", serve.ShortSHA(j.HeadSHA))
	}
	return append(out, attrs...)
}

// persistFinalReview upserts the terminal REST record under id. A no-op when id
// is empty (webhook/poll) or st is nil (REST disabled). Best-effort: a store
// failure is logged (redacted), never returned, so it can't fail the review.
//
// It derives a FRESH bounded context rather than reusing the job's ctx: a review
// that fails by TIMEOUT (the common case) leaves jobCtx already canceled, so a
// write on it would fail and strand the record at pending forever. The detached
// ctx guarantees the terminal status is recorded regardless of why the job ended.
func persistFinalReview(log *slog.Logger, st serve.ReviewStore, id, status string, out ReviewOutcome) {
	if id == "" || st == nil {
		return
	}
	ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 10*time.Second)
	defer cancel()
	headSHA := ""
	if out.PR != nil {
		headSHA = out.PR.HeadSHA
	}
	if _, err := st.UpsertReview(ctx, store.ReviewRecord{
		ID:       id,
		Mode:     "pr",
		HeadSHA:  headSHA,
		Status:   status,
		Findings: serveFindingsToEngine(out.Findings),
		Stats:    out.Stats,
	}); err != nil {
		log.Error("rest: persist final review failed", "id", id, "status", status, "err", config.RedactString(err.Error()))
	}
}

// serveFindingsToEngine maps the cli finding shape to engine.Finding (identical
// fields) for store persistence.
func serveFindingsToEngine(in []ReviewFinding) []engine.Finding {
	out := make([]engine.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, engine.Finding{
			File:           f.File,
			Line:           f.Line,
			EndLine:        f.EndLine,
			Severity:       f.Severity,
			Category:       f.Category,
			Rationale:      f.Rationale,
			SuggestedPatch: f.SuggestedPatch,
			QuotedCode:     f.QuotedCode,
		})
	}
	return out
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
	config.SetReviewValidators(engine.ValidGate, ghub.ValidFilterMode, ghub.ValidMinSeverity, ghub.ValidFormat, engine.ValidPromptFormat)
}
