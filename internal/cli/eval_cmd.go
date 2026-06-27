package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vanducng/miu-cr/internal/eval"
)

const defaultEvalTimeout = 30 * time.Minute

func evalCommand(opts *options) *cobra.Command {
	var (
		casesPath string
		toolFlags []string
	)
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run finding-recall evaluations against reviewer commands",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(casesPath) == "" {
				return &CLIError{Code: "eval.cases_required", Message: "--cases is required", Hint: "pass a JSON eval suite", Exit: 2}
			}
			tools, err := parseEvalTools(toolFlags)
			if err != nil {
				return err
			}
			suite, err := eval.LoadSuite(casesPath)
			if err != nil {
				return &CLIError{Code: "eval.cases_invalid", Message: err.Error(), Hint: "check the --cases JSON file", Exit: 2}
			}
			if !cmd.Flags().Changed("timeout") {
				opts.timeout = defaultEvalTimeout
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			result := eval.Run(ctx, suite, tools, opts.timeout)
			return writeSuccess(cmd.OutOrStdout(), "eval", "eval.result", result, evalSummary(result))
		},
	}
	cmd.Flags().StringVar(&casesPath, "cases", "", "JSON suite file")
	cmd.Flags().StringArrayVar(&toolFlags, "tool", nil, "Tool as name=command; repeat to compare tools")
	return cmd
}

func parseEvalTools(flags []string) ([]eval.Tool, error) {
	if len(flags) == 0 {
		return nil, &CLIError{Code: "eval.tool_required", Message: "--tool is required", Hint: `pass --tool 'miucr=miucr review --repo "$MIUCR_EVAL_REPO" --from "$MIUCR_EVAL_FROM" --to "$MIUCR_EVAL_TO" --gate none --no-save -o json'`, Exit: 2}
	}
	tools := make([]eval.Tool, 0, len(flags))
	seen := map[string]bool{}
	for _, flag := range flags {
		name, command, ok := strings.Cut(flag, "=")
		name = strings.TrimSpace(name)
		command = strings.TrimSpace(command)
		if !ok || name == "" || command == "" {
			return nil, &CLIError{Code: "eval.tool_invalid", Message: "tool must be name=command", Hint: "repeat --tool for each reviewer", Exit: 2}
		}
		if seen[name] {
			return nil, &CLIError{Code: "eval.tool_duplicate", Message: "duplicate tool " + name, Hint: "use unique tool names", Exit: 2}
		}
		seen[name] = true
		tools = append(tools, eval.Tool{Name: name, Command: command})
	}
	return tools, nil
}

func evalSummary(result eval.Result) map[string]any {
	out := map[string]any{"tools": len(result.Tools)}
	if len(result.Tools) == 1 {
		s := result.Tools[0].Summary
		out["cases"] = s.Cases
		out["expected"] = s.Expected
		out["matched"] = s.Matched
		out["precision"] = s.Precision
		out["recall"] = s.Recall
		out["f1"] = s.F1
		out["failed_cases"] = s.FailedCases
	}
	return out
}
