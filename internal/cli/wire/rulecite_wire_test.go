package wire

import (
	"path/filepath"
	"testing"

	"github.com/vanducng/miu-cr/internal/rules"
)

// ruleCitations links ONLY repo (RepoUntrusted) rules, converting their absolute
// Path to a repo-relative one via filepath.Rel; user (absolute home path) and
// built-in (defaults/* virtual path) rules are cite-only — never given a path,
// so a home dir or a non-repo path can never reach blobURL.
func TestRuleCitationsLinksRepoOnly(t *testing.T) {
	repo := "/tmp/clone"
	repoRule := filepath.Join(repo, ".miu", "cr", "rules", "go.md")
	loaded := []rules.Rule{
		{Stem: "go", Path: repoRule, Provenance: rules.RepoUntrusted},
		{Stem: "team", Path: "/home/alice/.config/miu/cr/rules/team.md", Provenance: rules.UserTrusted},
		{Stem: "security", Path: "defaults/security.md", Provenance: rules.BuiltinDefault},
	}

	cites := ruleCitations(loaded, repo)

	go_, ok := cites["go"]
	if !ok || !go_.Linkable {
		t.Fatalf("repo rule must be linkable: %+v", go_)
	}
	if go_.RepoRelPath != ".miu/cr/rules/go.md" {
		t.Fatalf("repo rule path must be repo-ROOT-relative (blobURL anchors at root), got %q", go_.RepoRelPath)
	}

	team, ok := cites["team"]
	if !ok || team.Linkable || team.RepoRelPath != "" {
		t.Fatalf("user rule must be cite-only with no path (home leak): %+v", team)
	}

	sec, ok := cites["security"]
	if !ok || sec.Linkable || sec.RepoRelPath != "" {
		t.Fatalf("builtin rule must be cite-only with no path: %+v", sec)
	}
}

// On a fork PR loadRules drops every RepoUntrusted rule, so the loaded set the
// citation map is built from carries no repo rule → nothing is linkable.
func TestRuleCitationsForkNoRepoLink(t *testing.T) {
	// Post-fork-drop set: only trusted layers survive.
	loaded := []rules.Rule{
		{Stem: "team", Path: "/home/alice/.config/miu/cr/rules/team.md", Provenance: rules.UserTrusted},
		{Stem: "security", Path: "defaults/security.md", Provenance: rules.BuiltinDefault},
	}
	cites := ruleCitations(loaded, "/tmp/clone")
	for stem, c := range cites {
		if c.Linkable {
			t.Fatalf("no rule may be linkable on a fork PR, but %q is: %+v", stem, c)
		}
	}
}
