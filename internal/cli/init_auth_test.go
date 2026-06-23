package cli

import (
	"bytes"
	stdctx "context"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/config"
)

// For --provider openai the menu must offer browser login (OAuth, ChatGPT plan)
// as option 1 — before the API-key paths — so the no-key path is the default.
func TestChooseAuthOpenAIOffersOAuthFirst(t *testing.T) {
	var out bytes.Buffer
	prof := config.Provider{Kind: config.KindOpenAI}
	// Pick env (2) so the actual OAuth browser flow never runs; we only assert
	// the rendered menu order.
	ask := func(string, string) string { return "2" }

	if _, err := chooseAuth(stdctx.Background(), ask, &out, authInput{}, "openai", &prof); err != nil {
		t.Fatalf("chooseAuth openai: %v", err)
	}

	menu := out.String()
	oauthAt := strings.Index(menu, "Browser login (OAuth)")
	envAt := strings.Index(menu, "OPENAI_API_KEY")
	if oauthAt < 0 {
		t.Fatalf("openai menu missing browser OAuth option:\n%s", menu)
	}
	if envAt < 0 {
		t.Fatalf("openai menu missing env-var option:\n%s", menu)
	}
	if oauthAt > envAt {
		t.Fatalf("openai menu must offer OAuth before the API key:\n%s", menu)
	}
	if !strings.Contains(menu, "no API key") {
		t.Fatalf("openai OAuth option should advertise the no-key ChatGPT plan:\n%s", menu)
	}
}

// anthropic must never offer OAuth (subscription OAuth is ToS-prohibited).
func TestChooseAuthAnthropicHasNoOAuth(t *testing.T) {
	var out bytes.Buffer
	prof := config.Provider{Kind: config.KindAnthropic}
	ask := func(string, string) string { return "1" } // env var (default)

	if _, err := chooseAuth(stdctx.Background(), ask, &out, authInput{}, "anthropic", &prof); err != nil {
		t.Fatalf("chooseAuth anthropic: %v", err)
	}
	if strings.Contains(out.String(), "OAuth") {
		t.Fatalf("anthropic menu must not offer OAuth:\n%s", out.String())
	}
}
