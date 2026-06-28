package engine

import (
	stdctx "context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
	enginectx "github.com/vanducng/miu-cr/internal/engine/context"
	"github.com/vanducng/miu-cr/internal/engine/diff"
)

const (
	defaultSubagentMaxParallel     = 2
	defaultSubagentMinFiles        = 8
	defaultSubagentMinContextBytes = 60000
	maxSubagentParallel            = 8
)

type reviewSharedContext struct {
	rulesText       string
	semanticContext string
	projectContext  string
	relatedContext  string
	rev             string
	trace           *ReviewTrace
}

type subagentPlan struct {
	name           string
	operatorPrompt string
	files          []diff.Diff
}

type subagentRunResult struct {
	name    string
	files   int
	out     ReviewOutput
	ms      int64
	err     error
	context enginectx.AssembledContext
	trace   *ReviewTrace
}

func (e *Engine) reviewPasses(ctx stdctx.Context, req Request, selected []diff.Diff, assembled enginectx.AssembledContext, shared reviewSharedContext) (ReviewOutput, int64, map[string]any, error) {
	if !useSubagents(req.Subagents, len(selected), len(assembled.Text)) {
		out, ms, err := e.reviewOnce(ctx, req, assembled.Text, shared, req.OperatorPrompt, req.Instruction, shared.trace)
		return out, ms, nil, err
	}

	plans := planSubagents(req.Subagents, selected)
	if len(plans) == 0 {
		out, ms, err := e.reviewOnce(ctx, req, assembled.Text, shared, req.OperatorPrompt, req.Instruction, shared.trace)
		return out, ms, nil, err
	}

	req.progress(fmt.Sprintf("reviewing with %d subagents...", len(plans)))
	start := time.Now()
	results := runSubagentPlans(ctx, e, req, plans, shared)
	mergeSubagentTraces(shared.trace, results)
	elapsedMS := time.Since(start).Milliseconds()
	stats := subagentStats(results, elapsedMS, req.Subagents.RequireAll)
	out, firstErr, okCount := mergeSubagentOutputs(results)
	if okCount == 0 && firstErr != nil {
		return ReviewOutput{}, elapsedMS, stats, firstErr
	}
	if firstErr != nil {
		stats["subagents_error"] = config.RedactString(firstErr.Error())
	}
	return out, elapsedMS, stats, nil
}

func (e *Engine) reviewOnce(ctx stdctx.Context, req Request, text string, shared reviewSharedContext, operatorPrompt, instruction string, trace *ReviewTrace) (ReviewOutput, int64, error) {
	start := time.Now()
	out, err := e.Agent.Review(ctx, AgentContext{
		Text:            text,
		Rules:           shared.rulesText,
		SemanticContext: shared.semanticContext,
		ProjectContext:  shared.projectContext,
		RelatedContext:  shared.relatedContext,
		WantDiagram:     req.WantDiagram,
		Instruction:     instruction,
		Conversation:    req.Conversation,
		OperatorPrompt:  operatorPrompt,
		RepoDir:         req.RepoDir,
		Rev:             shared.rev,
		Runner:          e.Runner,
		Progress:        req.Progress,
		Trace:           trace,
	})
	return out, time.Since(start).Milliseconds(), err
}

func useSubagents(cfg SubagentConfig, fileCount, contextBytes int) bool {
	if len(cfg.Agents) == 0 {
		return false
	}
	switch cfg.Mode {
	case "always":
		return true
	case "auto":
		minFiles := cfg.MinFiles
		if minFiles == 0 {
			minFiles = defaultSubagentMinFiles
		}
		minBytes := cfg.MinContextBytes
		if minBytes == 0 {
			minBytes = defaultSubagentMinContextBytes
		}
		return fileCount >= minFiles || contextBytes >= minBytes
	case "", "off":
		return false
	default:
		return false
	}
}

func planSubagents(cfg SubagentConfig, selected []diff.Diff) []subagentPlan {
	plans := make([]subagentPlan, 0, len(cfg.Agents)+1)
	assigned := make(map[string]bool, len(selected))
	for _, spec := range cfg.Agents {
		files := SelectFiles(selected, FilterOptions{Include: spec.IncludeGlobs, Exclude: spec.ExcludeGlobs})
		if len(files) == 0 {
			continue
		}
		for _, f := range files {
			assigned[f.NewPath] = true
		}
		plans = append(plans, subagentPlan{name: spec.Name, operatorPrompt: spec.OperatorPrompt, files: files})
	}
	var rest []diff.Diff
	for _, f := range selected {
		if !assigned[f.NewPath] {
			rest = append(rest, f)
		}
	}
	if len(rest) > 0 {
		plans = append(plans, subagentPlan{name: "default", files: rest})
	}
	return plans
}

func runSubagentPlans(ctx stdctx.Context, e *Engine, req Request, plans []subagentPlan, shared reviewSharedContext) []subagentRunResult {
	results := make([]subagentRunResult, len(plans))
	limit := req.Subagents.MaxParallel
	if limit <= 0 {
		limit = defaultSubagentMaxParallel
	}
	if limit > maxSubagentParallel {
		limit = maxSubagentParallel
	}
	if limit > len(plans) {
		limit = len(plans)
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, plan := range plans {
		wg.Add(1)
		go func(i int, plan subagentPlan) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[i] = subagentRunResult{name: plan.name, files: len(plan.files), err: ctx.Err()}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			assembled := enginectx.AssembleContext(plan.files, enginectx.AssembleOptions{
				TokenBudget:  subagentDiffBudget(req, shared),
				ExpandWindow: req.ExpandWindow,
			})
			var trace *ReviewTrace
			if shared.trace != nil {
				trace = &ReviewTrace{Sink: shared.trace.Sink}
			}
			out, ms, err := e.reviewOnce(ctx, req, assembled.Text, shared, joinPrompt(req.OperatorPrompt, plan.operatorPrompt), subagentInstruction(req.Instruction, plan), trace)
			results[i] = subagentRunResult{
				name:    plan.name,
				files:   len(plan.files),
				out:     out,
				ms:      ms,
				err:     err,
				context: assembled,
				trace:   trace,
			}
		}(i, plan)
	}
	wg.Wait()
	return results
}

func subagentDiffBudget(req Request, shared reviewSharedContext) int {
	budget := diffBudget(req.TokenBudget, req.RulesTokenBudget)
	if budget <= 0 {
		return budget
	}
	overhead := estContextTokens(shared.projectContext) + estContextTokens(shared.semanticContext) + estContextTokens(shared.relatedContext)
	if overhead <= 0 {
		return budget
	}
	if overhead >= budget {
		return 1
	}
	return budget - overhead
}

func estContextTokens(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(s) / 4
}

func mergeSubagentTraces(dst *ReviewTrace, results []subagentRunResult) {
	if dst == nil {
		return
	}
	var prompts []string
	var responses []string
	turnOffset := 0
	for _, r := range results {
		if r.trace == nil {
			continue
		}
		dst.SetSystemPrompt(r.trace.SystemPrompt)
		dst.SetModel(r.trace.Provider, r.trace.Model)
		if strings.TrimSpace(r.trace.UserPrompt) != "" {
			prompts = append(prompts, fmt.Sprintf("Subagent %q:\n%s", r.name, r.trace.UserPrompt))
		}
		if strings.TrimSpace(r.trace.FinalResponse) != "" {
			responses = append(responses, fmt.Sprintf("Subagent %q:\n%s", r.name, r.trace.FinalResponse))
		}
		minTurn, maxTurn := -1, -1
		for _, tr := range r.trace.Turns {
			if minTurn == -1 || tr.Turn < minTurn {
				minTurn = tr.Turn
			}
		}
		for _, tr := range r.trace.Turns {
			turn := tr.Turn - minTurn
			dst.RecordTool(turn+turnOffset, tr.Tool, tr.Args)
			if turn > maxTurn {
				maxTurn = turn
			}
		}
		if maxTurn >= 0 {
			turnOffset += maxTurn + 1
		}
	}
	dst.SetPrompt(strings.Join(prompts, "\n\n"))
	if len(responses) > 0 {
		dst.SetFinalResponse(strings.Join(responses, "\n\n"))
	}
}

func subagentStats(results []subagentRunResult, totalMS int64, requireAll bool) map[string]any {
	items := make([]any, 0, len(results))
	failedCount := 0
	for _, r := range results {
		status := "done"
		if r.err != nil {
			status = "failed"
			failedCount++
		}
		items = append(items, map[string]any{
			"name":             r.name,
			"status":           status,
			"files_reviewed":   float64(r.files),
			"context_bytes":    float64(len(r.context.Text)),
			"provider_ms":      float64(r.ms),
			"findings_total":   float64(len(r.out.Findings)),
			"truncation_level": r.context.Stats["truncation_level"],
		})
	}
	return map[string]any{
		"subagents_enabled":     true,
		"subagent_count":        float64(len(results)),
		"subagents_failed":      float64(failedCount),
		"subagents_degraded":    failedCount > 0 && requireAll,
		"subagents_require_all": requireAll,
		"subagents":             items,
		"subagents_ms":          float64(totalMS),
	}
}

func mergeSubagentOutputs(results []subagentRunResult) (ReviewOutput, error, int) {
	var out ReviewOutput
	var parts []string
	out.FileSummaries = map[string]string{}
	var errs []error
	okCount := 0
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		okCount++
		out.Findings = append(out.Findings, r.out.Findings...)
		if walkthrough := strings.TrimSpace(r.out.Walkthrough); walkthrough != "" {
			if strings.HasPrefix(walkthrough, "- ") {
				walkthrough = "- " + r.name + ": " + strings.TrimSpace(strings.TrimPrefix(walkthrough, "- "))
			} else {
				walkthrough = fmt.Sprintf("%s: %s", r.name, walkthrough)
			}
			parts = append(parts, walkthrough)
		}
		for k, v := range r.out.FileSummaries {
			if _, ok := out.FileSummaries[k]; !ok {
				out.FileSummaries[k] = v
			}
		}
		if out.Diagram == "" {
			out.Diagram = r.out.Diagram
		}
		if r.out.Confidence > 0 && (out.Confidence == 0 || r.out.Confidence < out.Confidence) {
			out.Confidence = r.out.Confidence
			out.ConfidenceReason = r.out.ConfidenceReason
		}
		out.Usage.Add(r.out.Usage)
	}
	out.Walkthrough = strings.Join(parts, "\n\n")
	if len(out.FileSummaries) == 0 {
		out.FileSummaries = nil
	}
	return out, errors.Join(errs...), okCount
}

func joinPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}

func subagentInstruction(base string, plan subagentPlan) string {
	scope := fmt.Sprintf("Subagent %q: review only this subagent's diff scope. Use tools only for context that affects these files.", plan.name)
	if strings.TrimSpace(base) == "" {
		return scope
	}
	return strings.TrimSpace(base) + "\n\n" + scope
}

func SubagentsDegraded(stats map[string]any) bool {
	if stats == nil {
		return false
	}
	v, _ := stats["subagents_degraded"].(bool)
	return v
}
