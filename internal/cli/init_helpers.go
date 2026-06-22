package cli

import (
	"bufio"
	stdctx "context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vanducng/miu-cr/internal/config"
	"github.com/vanducng/miu-cr/internal/rules"
)

// initAbort handles a Ctrl+C (context cancel) mid-prompt: print a notice and exit
// 130 (128+SIGINT). Overridable in tests so a cancelled prompt doesn't kill the
// test process.
var initAbort = func(out io.Writer) {
	fmt.Fprintln(out, "\n  aborted.")
	os.Exit(130)
}

// askLine prints "prompt [def]: " and returns the trimmed answer, or def on a
// blank line / EOF. The read runs in a goroutine so a cancelled ctx (Ctrl+C)
// interrupts the prompt instead of blocking forever on stdin.
func askLine(ctx stdctx.Context, in *bufio.Scanner, out io.Writer, prompt, def string) string {
	if def != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}
	type res struct {
		ok   bool
		text string
	}
	ch := make(chan res, 1)
	go func() {
		if in.Scan() {
			ch <- res{true, in.Text()}
			return
		}
		ch <- res{false, ""}
	}()
	select {
	case <-ctx.Done():
		initAbort(out)
		return def // unreachable in prod (initAbort exits); a no-op in tests
	case r := <-ch:
		if !r.ok {
			return def
		}
		if v := strings.TrimSpace(r.text); v != "" {
			return v
		}
		return def
	}
}

// askChoice re-prompts until the answer (lowercased) is one of valid; a blank
// answer returns def. Stops a fat-fingered entry from silently falling through to
// a default branch. Ctrl+C still aborts (askLine watches the context).
func askChoice(ask func(string, string) string, prompt, def string, valid ...string) string {
	for {
		a := strings.ToLower(strings.TrimSpace(ask(prompt, def)))
		if a == "" {
			a = def
		}
		for _, v := range valid {
			if a == v {
				return a
			}
		}
	}
}

// confirm asks a yes/no question, returning def on a blank answer.
func confirm(ask func(string, string) string, prompt string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	ans := strings.ToLower(strings.TrimSpace(ask(prompt+" ["+d+"]", "")))
	if ans == "" {
		return def
	}
	return ans == "y" || ans == "yes"
}

// detectStem stats build manifests in cwd and returns the rule-file stem for the
// detected stack, or "" when none match. Detection runs ONLY here, never on the
// review hot path.
func detectStem(dir string) string {
	switch {
	case exists(dir, "go.mod"):
		return "go"
	case exists(dir, "package.json"):
		return "typescript"
	case exists(dir, "pyproject.toml"), exists(dir, "setup.py"):
		return "python"
	default:
		return ""
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// scaffoldDetectedRules writes a project rules file for the detected stack (or a
// generic one). Mirrors rules_cmd.go's MkdirAll + Stat + --force + WriteFile.
// Returns the written path, or "" when skipped.
func scaffoldDetectedRules(ask func(string, string) string, nonInteractive, force bool) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "." // detect against the relative cwd rather than silently skipping
	}
	stem := detectStem(cwd)
	if stem == "" {
		stem = "rules"
	}
	rel := filepath.Join(".miu", "cr", "rules", stem+".md")

	if !nonInteractive {
		if !confirm(ask, "Scaffold "+rel+"?", true) {
			return "", nil
		}
	}

	dir := filepath.Dir(rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", &CLIError{Code: "init.rules_mkdir", Message: "create rules dir: " + err.Error(), Hint: "check write permissions for " + dir, Exit: 1}
	}
	if _, statErr := os.Stat(rel); statErr == nil && !force {
		return rel, nil // already there; leave it
	}
	if err := os.WriteFile(rel, []byte(rules.RuleTemplate()), 0o644); err != nil {
		return "", &CLIError{Code: "init.rules_write", Message: "write rules: " + err.Error(), Hint: "check write permissions for " + rel, Exit: 1}
	}
	return rel, nil
}

// printPayoff renders the success box ending on the literal first-review command.
func printPayoff(out io.Writer, path, provider string, prof config.Provider, rulePath string, method authMethod) {
	var auth, note string
	switch method {
	case authMethodOAuth:
		auth = "browser login (OAuth) — reviews run on your ChatGPT plan"
	case authMethodPaste:
		auth = "inline auth_token (plaintext on disk)"
	default:
		auth = "env " + prof.AuthEnv
		note = "  Set " + prof.AuthEnv + " in your shell before reviewing."
	}
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "  ✓ Config written: %s\n", path)
	fmt.Fprintf(out, "  ✓ Provider: %s\n", provider)
	fmt.Fprintf(out, "  ✓ Auth: %s\n", auth)
	if rulePath != "" {
		fmt.Fprintf(out, "  ✓ Rules: %s\n", rulePath)
	}
	fmt.Fprintln(out, "")
	if note != "" {
		fmt.Fprintln(out, note)
	}
	fmt.Fprintln(out, "  ▶ miucr review --staged")
	fmt.Fprintln(out, "")
}
