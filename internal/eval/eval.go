package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/vanducng/miu-cr/internal/config"
)

type Suite struct {
	Cases []Case `json:"cases"`
}

type Case struct {
	ID       string    `json:"id"`
	Repo     string    `json:"repo,omitempty"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	Commit   string    `json:"commit,omitempty"`
	Expected []Finding `json:"expected"`
}

type Tool struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type Finding struct {
	ID       string `json:"id,omitempty"`
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	EndLine  int    `json:"end_line,omitempty"`
	Severity string `json:"severity,omitempty"`
	Category string `json:"category,omitempty"`
	Title    string `json:"title,omitempty"`
}

func (f *Finding) UnmarshalJSON(b []byte) error {
	type raw Finding
	var aux struct {
		raw
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*f = Finding(aux.raw)
	if f.File == "" {
		f.File = aux.Path
	}
	if f.Line == 0 {
		f.Line = aux.StartLine
	}
	if f.Title == "" {
		f.Title = aux.Message
	}
	return nil
}

type Result struct {
	Tools []ToolResult `json:"tools"`
}

type ToolResult struct {
	Name    string       `json:"name"`
	Summary Score        `json:"summary"`
	Cases   []CaseResult `json:"cases"`
}

type CaseResult struct {
	ID         string         `json:"id"`
	ExitCode   int            `json:"exit_code"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
	Score      Score          `json:"score"`
	Findings   []Finding      `json:"findings"`
	Missed     []Finding      `json:"missed"`
	Stats      map[string]any `json:"stats,omitempty"`
}

type Score struct {
	Cases         int     `json:"cases,omitempty"`
	LabeledCases  int     `json:"labeled_cases,omitempty"`
	Expected      int     `json:"expected"`
	Found         int     `json:"found"`
	Matched       int     `json:"matched"`
	Missed        int     `json:"missed"`
	FalsePositive int     `json:"false_positives"`
	Precision     float64 `json:"precision"`
	Recall        float64 `json:"recall"`
	F1            float64 `json:"f1"`
	DurationMS    int64   `json:"duration_ms,omitempty"`
	FailedCases   int     `json:"failed_cases,omitempty"`
	scoredFound   int
}

func LoadSuite(path string) (Suite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	var suite Suite
	if err := json.Unmarshal(b, &suite); err != nil {
		return Suite{}, err
	}
	for i := range suite.Cases {
		if strings.TrimSpace(suite.Cases[i].ID) == "" {
			return Suite{}, fmt.Errorf("case %d has empty id", i)
		}
	}
	return suite, nil
}

func Run(ctx context.Context, suite Suite, tools []Tool, timeout time.Duration) Result {
	out := Result{Tools: make([]ToolResult, 0, len(tools))}
	for _, tool := range tools {
		tr := ToolResult{Name: tool.Name, Cases: make([]CaseResult, 0, len(suite.Cases))}
		for i, c := range suite.Cases {
			cr := runCase(ctx, c, i, tool, timeout)
			tr.Cases = append(tr.Cases, cr)
			tr.Summary = addScore(tr.Summary, cr.Score)
			tr.Summary.Cases++
			tr.Summary.DurationMS += cr.DurationMS
			if cr.Error != "" {
				tr.Summary.FailedCases++
			}
		}
		tr.Summary = finalizeScore(tr.Summary)
		out.Tools = append(out.Tools, tr)
	}
	return out
}

func runCase(ctx context.Context, c Case, index int, tool Tool, timeout time.Duration) CaseResult {
	start := time.Now()
	if err := validateCaseEnv(c); err != nil {
		score, missed := Score{Found: 0}, []Finding(nil)
		if c.Expected != nil {
			score, missed = ScoreCase(c.Expected, nil)
		}
		return CaseResult{
			ID:         c.ID,
			DurationMS: time.Since(start).Milliseconds(),
			Error:      err.Error(),
			Score:      score,
			Missed:     missed,
		}
	}
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := shellCommand(runCtx, tool.Command)
	prepareCommand(cmd)
	if c.Repo != "" {
		cmd.Dir = c.Repo
	}
	cmd.Env = append(os.Environ(),
		"MIUCR_EVAL_CASE_ID="+c.ID,
		"MIUCR_EVAL_CASE_INDEX="+strconv.Itoa(index),
		"MIUCR_EVAL_REPO="+c.Repo,
		"MIUCR_EVAL_FROM="+c.From,
		"MIUCR_EVAL_TO="+c.To,
		"MIUCR_EVAL_COMMIT="+c.Commit,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	stopKill := context.AfterFunc(runCtx, func() { killCommand(cmd) })
	err := cmd.Run()
	stopKill()
	duration := time.Since(start).Milliseconds()

	exitCode := 0
	if err != nil {
		exitCode = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
	}
	parsed, parseErr := parseOutput(stdout.Bytes())
	errText := ""
	if err != nil {
		errText = strings.TrimSpace(config.RedactString(stderr.String()))
		if errText == "" {
			errText = parsed.Error
		}
		if errText == "" {
			errText = config.RedactString(err.Error())
		}
	}
	if parseErr != nil {
		if errText != "" {
			errText += "; "
		}
		errText += "parse findings: " + parseErr.Error()
	}
	score, missed := Score{Found: len(parsed.Findings)}, []Finding(nil)
	if c.Expected != nil {
		score, missed = ScoreCase(c.Expected, parsed.Findings)
	}
	return CaseResult{
		ID:         c.ID,
		ExitCode:   exitCode,
		DurationMS: duration,
		Error:      config.RedactString(errText),
		Score:      score,
		Findings:   parsed.Findings,
		Missed:     missed,
		Stats:      parsed.Stats,
	}
}

func validateCaseEnv(c Case) error {
	for name, value := range map[string]string{
		"id":     c.ID,
		"repo":   c.Repo,
		"from":   c.From,
		"to":     c.To,
		"commit": c.Commit,
	} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("case %s contains an unsupported environment character", name)
		}
	}
	return nil
}

type parsedOutput struct {
	Findings []Finding
	Stats    map[string]any
	Error    string
}

func parseOutput(b []byte) (parsedOutput, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return parsedOutput{}, fmt.Errorf("empty tool output")
	}
	var out struct {
		Findings []Finding      `json:"findings"`
		Stats    map[string]any `json:"stats"`
		OK       *bool          `json:"ok"`
		Error    struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
		Data struct {
			Findings []Finding      `json:"findings"`
			Stats    map[string]any `json:"stats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return parsedOutput{}, err
	}
	if out.OK != nil && !*out.OK {
		msg := strings.TrimSpace(out.Error.Code)
		if out.Error.Message != "" {
			if msg != "" {
				msg += ": "
			}
			msg += out.Error.Message
		}
		if out.Error.Hint != "" {
			msg += " (" + out.Error.Hint + ")"
		}
		return parsedOutput{Error: msg}, nil
	}
	if out.Findings != nil {
		return parsedOutput{Findings: out.Findings, Stats: out.Stats}, nil
	}
	return parsedOutput{Findings: out.Data.Findings, Stats: out.Data.Stats}, nil
}

func ScoreCase(expected, findings []Finding) (Score, []Finding) {
	used := make([]bool, len(findings))
	missed := make([]Finding, 0)
	matched := 0
	for _, exp := range expected {
		hit := false
		for i, got := range findings {
			if used[i] || !matches(exp, got) {
				continue
			}
			used[i] = true
			matched++
			hit = true
			break
		}
		if !hit {
			missed = append(missed, exp)
		}
	}
	falsePositive := len(findings) - matched
	if falsePositive < 0 {
		falsePositive = 0
	}
	score := Score{
		Expected:      len(expected),
		Found:         len(findings),
		LabeledCases:  1,
		Matched:       matched,
		Missed:        len(missed),
		FalsePositive: falsePositive,
		scoredFound:   len(findings),
	}
	return finalizeScore(score), missed
}

func matches(exp, got Finding) bool {
	if cleanPath(exp.File) != cleanPath(got.File) {
		return false
	}
	if exp.Line == 0 {
		return true
	}
	gotStart, gotEnd := got.Line, got.EndLine
	if gotStart == 0 {
		return false
	}
	if gotEnd == 0 {
		gotEnd = gotStart
	}
	expEnd := exp.EndLine
	if expEnd == 0 {
		expEnd = exp.Line
	}
	return gotStart <= expEnd && exp.Line <= gotEnd
}

func cleanPath(path string) string {
	return strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"), "./")
}

func addScore(a, b Score) Score {
	a.Expected += b.Expected
	a.Found += b.Found
	a.LabeledCases += b.LabeledCases
	a.Matched += b.Matched
	a.Missed += b.Missed
	a.FalsePositive += b.FalsePositive
	a.scoredFound += b.scoredFound
	return a
}

func finalizeScore(s Score) Score {
	if s.LabeledCases == 0 {
		return s
	}
	if s.Expected == 0 && s.scoredFound == 0 {
		s.Precision, s.Recall, s.F1 = 1, 1, 1
		return s
	}
	s.Precision = ratio(s.Matched, s.scoredFound)
	s.Recall = ratio(s.Matched, s.Expected)
	if s.Precision+s.Recall > 0 {
		s.F1 = 2 * s.Precision * s.Recall / (s.Precision + s.Recall)
	}
	return s
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
