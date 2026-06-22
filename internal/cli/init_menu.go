package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/vanducng/miu-cr/internal/config"
)

// menuItem is one selectable line: key) name<pad>desc, or key) desc when name="".
type menuItem struct {
	key, name, desc string
}

// printMenu renders a sectioned menu — blank line, indented title, one option per
// line with aligned descriptions — for a readable plain-terminal layout.
func printMenu(out io.Writer, title string, items []menuItem) {
	fmt.Fprintf(out, "\n  %s\n", title)
	for _, it := range items {
		if it.name != "" {
			fmt.Fprintf(out, "    %s) %-11s%s\n", it.key, it.name, it.desc)
		} else {
			fmt.Fprintf(out, "    %s) %s\n", it.key, it.desc)
		}
	}
	fmt.Fprintln(out)
}

// chooseProvider resolves the provider name + a profile seeded from Defaults.
// Interactive: a sectioned select (anthropic default | openai | custom). Custom
// asks kind + base_url; auth handled in chooseAuth. Non-interactive reads
// flagProvider.
func chooseProvider(ask func(string, string) string, out io.Writer, nonInteractive bool, flagProvider, flagBaseURL string) (string, config.Provider, error) {
	base := config.Defaults()
	name := strings.TrimSpace(flagProvider)
	if !nonInteractive {
		printMenu(out, "Select a provider:", []menuItem{
			{"1", "anthropic", "Claude — API key"},
			{"2", "openai", "OpenAI / ChatGPT — browser login or API key"},
			{"3", "custom", "OpenAI-compatible gateway — API key + base URL"},
		})
		switch askChoice(ask, "  Provider", "1", "1", "2", "3", "anthropic", "openai", "custom") {
		case "2", "openai":
			name = "openai"
		case "3", "custom":
			name = "custom"
		default:
			name = "anthropic"
		}
	}
	if name == "" {
		name = "anthropic"
	}

	if prof, ok := base.Providers[name]; ok {
		if flagBaseURL != "" {
			prof.BaseURL = flagBaseURL
		}
		return name, prof, nil
	}
	if name != "custom" && nonInteractive {
		return "", config.Provider{}, &CLIError{Code: "init.aborted", Message: "unknown --provider " + name, Hint: "use anthropic, openai, or custom", Exit: 2}
	}
	return chooseCustom(ask, out, flagBaseURL)
}

// chooseCustom builds a custom profile (existing kind + a user base_url) under a
// caller-named profile.
func chooseCustom(ask func(string, string) string, out io.Writer, flagBaseURL string) (string, config.Provider, error) {
	printMenu(out, "Custom gateway kind:", []menuItem{
		{"1", "anthropic", "Anthropic-compatible API"},
		{"2", "openai", "OpenAI-compatible API"},
	})
	prof := config.Provider{Kind: config.KindAnthropic, Model: config.DefaultAnthropicModel}
	if k := strings.ToLower(ask("  Kind", "1")); k == "2" || k == "openai" {
		prof = config.Provider{Kind: config.KindOpenAI, Model: config.DefaultOpenAIModel}
	}
	bu := flagBaseURL
	if bu == "" {
		bu = strings.TrimSpace(ask("  Base URL (gateway endpoint)", ""))
	}
	prof.BaseURL = bu
	pname := strings.TrimSpace(ask("  Profile name", "custom"))
	if pname == "" {
		pname = "custom"
	}
	return pname, prof, nil
}
