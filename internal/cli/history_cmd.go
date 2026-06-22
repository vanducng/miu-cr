package cli

import (
	stdctx "context"
	"strings"

	"github.com/spf13/cobra"

	ghub "github.com/vanducng/miu-cr/internal/github"
	"github.com/vanducng/miu-cr/internal/store"
)

// historyCommand is the local review-history group: bare `history` lists recent
// reviews, with `show <id>` and `prune` subcommands. All run over the same store
// the review path persists to.
func historyCommand(opts *options) *cobra.Command {
	var a historyListArgs
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List, show, and prune saved reviews from the local history store",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistoryList(cmd, a)
		},
	}
	f := cmd.Flags()
	f.StringVar(&a.repo, "repo", "", "Filter by owner/repo (PR) or repo dir (local)")
	f.StringVar(&a.pr, "pr", "", "Filter by a PR ref: owner/repo#N")
	f.StringVar(&a.since, "since", "", "Only reviews newer than this: 7d, 24h, or 2026-06-01")
	f.IntVar(&a.limit, "limit", 20, "Max rows (0 = no limit)")
	cmd.AddCommand(historyShowCommand(opts))
	cmd.AddCommand(historyPruneCommand(opts))
	return cmd
}

type historyListArgs struct {
	repo  string
	pr    string
	since string
	limit int
}

func runHistoryList(cmd *cobra.Command, a historyListArgs) error {
	filter, err := buildFilter(a)
	if err != nil {
		return err
	}
	st, closeStore, err := openHistory(cmd.Context())
	if err != nil {
		return err
	}
	defer closeStore()
	rows, err := st.ListReviews(cmd.Context(), filter)
	if err != nil {
		return &CLIError{Code: "history.query_failed", Message: "list reviews: " + err.Error(), Exit: 1}
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, summaryRow(r))
	}
	if prettyOutput {
		return renderHistoryList(cmd.OutOrStdout(), rows)
	}
	return writeSuccess(cmd.OutOrStdout(), "history", "history.list", map[string]any{"reviews": items}, map[string]any{"count": len(items)})
}

func historyShowCommand(_ *options) *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one full saved review (findings, stats, transcript, raw I/O)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if id == "" {
				return &CLIError{Code: "history.id_required", Message: "a non-empty id is required", Hint: "miucr history show <id>", Exit: 2}
			}
			st, closeStore, err := openHistory(cmd.Context())
			if err != nil {
				return err
			}
			defer closeStore()
			rec, err := st.GetReview(cmd.Context(), id)
			if err != nil {
				return &CLIError{Code: "history.not_found", Message: err.Error(), Hint: "run `miucr history` to list ids", Exit: 2, Details: map[string]any{"id": id}}
			}
			data := recordData(rec)
			summary := map[string]any{"id": rec.ID, "findings": len(rec.Findings), "max_severity": maxSeverityOf(rec)}
			if prettyOutput {
				return renderHistoryRecord(cmd.OutOrStdout(), rec, raw)
			}
			return writeSuccess(cmd.OutOrStdout(), "history show", "history.record", data, summary)
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "Show the raw prompt/response prominently (pretty mode)")
	return cmd
}

func historyPruneCommand(_ *options) *cobra.Command {
	var (
		keep      int
		olderThan string
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete old saved reviews (--keep N and/or --older-than 30d)",
		RunE: func(cmd *cobra.Command, args []string) error {
			pol := store.PrunePolicy{Keep: keep}
			if olderThan != "" {
				cutoff, err := parseSince(olderThan)
				if err != nil {
					return err
				}
				pol.OlderThan = cutoff
			}
			if pol.Keep <= 0 && pol.OlderThan.IsZero() {
				return &CLIError{Code: "history.prune_policy_required", Message: "prune needs at least one of --keep or --older-than", Hint: "e.g. --keep 100 or --older-than 30d", Exit: 2}
			}
			if !yes {
				return &CLIError{Code: "history.prune_confirm_required", Message: "prune is destructive; pass --yes to proceed", Hint: "miucr history prune --keep N --yes", Exit: 2}
			}
			st, closeStore, err := openHistory(cmd.Context())
			if err != nil {
				return err
			}
			defer closeStore()
			n, err := st.PruneReviews(cmd.Context(), pol)
			if err != nil {
				return &CLIError{Code: "history.query_failed", Message: "prune reviews: " + err.Error(), Exit: 1}
			}
			data := map[string]any{"deleted": n}
			return writeSuccess(cmd.OutOrStdout(), "history prune", "history.prune", data, map[string]any{"deleted": n})
		},
	}
	f := cmd.Flags()
	f.IntVar(&keep, "keep", 0, "Keep the newest N reviews, delete the rest")
	f.StringVar(&olderThan, "older-than", "", "Delete reviews older than this: 30d, 24h, or 2026-06-01")
	f.BoolVar(&yes, "yes", false, "Confirm the destructive delete")
	return cmd
}

func openHistory(ctx stdctx.Context) (store.Store, func(), error) {
	if historyStoreFactory == nil {
		return nil, nil, &CLIError{Code: "history.unavailable", Message: "history store is not wired", Exit: 1}
	}
	st, closeStore, err := historyStoreFactory(ctx)
	if err != nil {
		return nil, nil, &CLIError{Code: "history.unavailable", Message: "open history store: " + err.Error(), Exit: 1}
	}
	return st, closeStore, nil
}

func buildFilter(a historyListArgs) (store.ReviewFilter, error) {
	f := store.ReviewFilter{Repo: strings.TrimSpace(a.repo), Limit: a.limit}
	if pr := strings.TrimSpace(a.pr); pr != "" {
		ref, err := ghub.ParseRef(pr)
		if err != nil {
			return f, &CLIError{Code: "history.bad_pr", Message: "invalid --pr: " + err.Error(), Hint: "use owner/repo#N", Exit: 2}
		}
		f.Owner, f.Repo, f.Number = ref.Owner, ref.Repo, ref.Number
	}
	if s := strings.TrimSpace(a.since); s != "" {
		t, err := parseSince(s)
		if err != nil {
			return f, err
		}
		f.Since = t
	}
	return f, nil
}
