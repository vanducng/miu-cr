#!/usr/bin/env bash
# Runs inside `asciinema rec`. $1 = demo root, $2 = dir holding the miucr binary.
set -u
root=$1
export HOME="$root/home" XDG_CONFIG_HOME="$root/home/.config" PATH="$2:$PATH" GIT_CONFIG_GLOBAL=/dev/null GIT_PAGER=cat PAGER=cat
unset GITHUB_TOKEN GH_TOKEN ANTHROPIC_API_KEY OPENAI_API_KEY
rm -rf "$root/home" "$root/shop" && mkdir -p "$root/home" "$root/shop/api" && cd "$root/shop"
git init -q -b main && git config user.name demo && git config user.email demo@example.com
printf 'module example.com/shop\n\ngo 1.25\n' > go.mod
cat > api/auth.go <<'GO'
package api

import "net/http"

func isAdmin(r *http.Request) bool { return r.Header.Get("X-Role") == "admin" }

func secretReport() string { return "revenue: 42" }
GO
cat > api/admin.go <<'GO'
package api

import "net/http"

// Admin serves the internal report to admins only.
func Admin(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Write([]byte(secretReport()))
}
GO
git add -A && git commit -qm "admin report"
perl -0pi -e 's/\t\treturn\n//' api/admin.go
git add api/admin.go

note() { printf '\033[2m# %s\033[0m\n' "$1"; sleep "${2:-0.8}"; }
run() {
  printf '\033[1;32m$\033[0m '
  local s=$1 i
  for ((i = 0; i < ${#s}; i++)); do printf '%s' "${s:i:1}"; sleep 0.02; done
  sleep 0.35
  printf '\n'
  eval "$s"
  sleep "${2:-2}"
}

clear
note "miucr: AI code review for your terminal, CI, and agents"
note "install: curl -fsSL https://cr.miu.sh/install.sh | sh" 1.2
run "miucr version | jq -r .data.version" 1.5
clear
note "1. set up: any Anthropic-compatible gateway, key read from env at runtime"
run "miucr init --non-interactive --provider custom --base-url https://api.z.ai/api/anthropic --auth env --auth-env ZAI_API_KEY --no-rules --yes 2>/dev/null | jq -c '.data | {default_provider, auth_env, next}'" 3
run "miucr rules check api/admin.go | jq -c '.data'" 3
clear
note "2. review what you are about to commit; --gate high exits 2 on a serious finding"
run "git diff --cached --stat" 1
run "miucr review --staged --model glm-5.2 --gate high -o pretty; echo \"exit=\$?\"" 6
clear
note "3. every review is saved"
run "miucr history | jq -r '.data.reviews[] | [.id, .mode, .max_severity, .findings] | @tsv'" 3
clear
note "4. dry-run a GitHub PR review (public repo, no token needed)"
run "miucr review --pr vanducng/miu-cr#330 --model glm-5.2 -o pretty" 6
note "add --post with GITHUB_TOKEN to publish inline comments and one summary" 2.5
clear
note "docs: cr.miu.sh  (append .md to any page for agents)" 2.5
