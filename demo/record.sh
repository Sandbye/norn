#!/usr/bin/env bash
# Regenerate assets/demo.gif for the README.
#
# Self-contained: builds norn into a throwaway dir, seeds a demo world (a repo +
# worktrees with .state.md, a session store, fake agent transcripts, and an
# isolated HOME with a generic config so nothing real leaks), records the TUI
# with VHS, and copies the result to assets/demo.gif.
#
# Requires: go, vhs (https://github.com/charmbracelet/vhs).
# Usage: demo/record.sh   (run from anywhere; paths resolve off the repo root)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASE="$(mktemp -d)"
trap 'rm -rf "$BASE"' EXIT

REPO="$BASE/repo"; WT="$BASE/wt"; STATE="$BASE/state"; CLAUDEC="$BASE/claude"; HOMED="$BASE/home"
mkdir -p "$REPO" "$WT" "$STATE/work" "$CLAUDEC/projects" "$HOMED/.config/work"

echo "building norn…"
go build -C "$REPO_ROOT" -o "$BASE/norn" ./cmd/norn

cat > "$HOMED/.config/work/config.yaml" <<EOF
worktree_dir: $WT
user: { name: you, email: you@example.com }
base_branches: [main]
theme: nord
EOF

git -C "$REPO" init -q -b main
git -C "$REPO" config user.email demo@example.com
git -C "$REPO" config user.name demo
printf '# app\n' > "$REPO/README.md"
git -C "$REPO" add -A && git -C "$REPO" commit -qm init

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
ago() { date -u -v-"$1" +%Y-%m-%dT%H:%M:%SZ; }

# branch|kind|title|goal|next|agent|pr
rows=(
  "feature/login-flow|task|SSO login flow|let users sign in with Google|wire the OAuth callback so it exchanges the code for tokens and persists the session|working|0"
  "feature/webhooks|task|Stripe webhooks|process payment events reliably|add retry with backoff|waiting|41"
  "fix/csv-export|task|CSV export encoding|export UTF-8 without a BOM|repro the mojibake on import|idle|0"
  "fix/redirect-loop|task|Redirect loop on logout|stop the infinite 302|clear the stale session cookie|working|38"
  "chore/deps-bump|task|Bump dependencies|get onto Go 1.23|run the suite green|idle|0"
  "epic/billing-revamp|task|Billing revamp|move to usage-based billing|split into subtasks|waiting|44"
  "review/pr-15|review|Review PR #15|approve or request changes|read the migration diff|idle|15"
)

sessions="["; first=1; i=0
for row in "${rows[@]}"; do
  IFS='|' read -r branch kind title goal next agent pr <<< "$row"
  path="$WT/$branch"
  git -C "$REPO" worktree add -q -b "$branch" "$path" main
  blocked="none"
  [ "$branch" = "feature/webhooks" ] && blocked="waiting on Stripe test keys"
  cat > "$path/.state.md" <<EOF
task:  $title
goal:  $goal
next:  $next
done:
  - scaffolding + types in place
  - happy-path wired end to end
  - unit tests green
blocked: $blocked
updated: $(now)
EOF
  slug="$(printf '%s' "$path" | sed 's#[/.]#-#g')"
  tdir="$CLAUDEC/projects/$slug"; mkdir -p "$tdir"
  case "$agent" in
    working) stop='"tool_use"'; ts="$(now)";;
    waiting) stop='"end_turn"'; ts="$(now)";;
    *)       stop='"end_turn"'; ts="$(ago 2H)";;
  esac
  printf '{"type":"system","timestamp":"%s"}\n' "$(ago 1d)" > "$tdir/seed.jsonl"
  printf '{"type":"assistant","timestamp":"%s","message":{"stop_reason":%s,"content":[{"type":"text","text":"x"}]}}\n' "$ts" "$stop" >> "$tdir/seed.jsonl"
  prline=""; [ "$pr" != "0" ] && prline="\"pr_number\": $pr,"
  [ "$first" = 1 ] && first=0 || sessions+=","
  sessions+="{ \"id\": \"repo:$branch\", \"repo\": \"repo\", \"branch\": \"$branch\", \"kind\": \"$kind\", \"path\": \"$path\", \"title\": \"$title\", $prline \"status\": \"active\", \"started_at\": \"$(ago 3d)\", \"last_activity_at\": \"$(ago $((i*7))M)\" }"
  i=$((i+1))
done
printf '{\n  "sessions": %s]\n}\n' "$sessions" > "$STATE/work/sessions.json"

cat > "$BASE/demo.tape" <<EOF
Output "$BASE/demo.gif"
Set FontSize 14
Set Width 1200
Set Height 720
Set Padding 18
Set Shell "zsh"
Set TypingSpeed 60ms
Hide
Type "cd $REPO"
Enter
Type "export HOME=$HOMED XDG_STATE_HOME=$STATE CLAUDE_CONFIG_DIR=$CLAUDEC"
Enter
Type "clear"
Enter
Sleep 800ms
Show
Type "$BASE/norn"
Enter
Sleep 4s
Down@700ms 3
Sleep 1200ms
Up@700ms 2
Sleep 1200ms
Type "/"
Sleep 400ms
Type "fix"
Sleep 1800ms
Escape
Sleep 900ms
Type "3"
Sleep 1800ms
Type "5"
Sleep 1800ms
Type "1"
Sleep 1600ms
Type "q"
Sleep 600ms
EOF

echo "recording…"
vhs "$BASE/demo.tape"
cp "$BASE/demo.gif" "$REPO_ROOT/assets/demo.gif"
echo "wrote $REPO_ROOT/assets/demo.gif"
