#!/bin/bash
# Claude Code status line: dir, git, model, context, cost, duration, rate limits, extras
input=$(cat)

j() { echo "$input" | jq -r "$1" 2>/dev/null; }

cwd=$(j '.workspace.current_dir // .cwd // empty')
model=$(j '.model.display_name // empty')
cost=$(j '.cost.total_cost_usd // empty')
dur_ms=$(j '.cost.total_duration_ms // empty')
added=$(j '.cost.total_lines_added // empty')
removed=$(j '.cost.total_lines_removed // empty')
used=$(j '.context_window.used_percentage // empty')
effort=$(j '.effort.level // empty')
five=$(j '.rate_limits.five_hour.used_percentage // empty')
week=$(j '.rate_limits.seven_day.used_percentage // empty')
pr=$(j '.pr.number // empty')
pr_state=$(j '.pr.review_state // empty')
pr_kind=$(j '.pr.kind // empty')
wt=$(j '.workspace.git_worktree // empty')
vim=$(j '.vim.mode // empty')
cold=$(j 'if .prompt_cache.caching_observed == true and .prompt_cache.warm == false then "cold" else empty end')

C_RESET=$'\033[0m'
C_BLUE=$'\033[34m'
C_CYAN=$'\033[36m'
C_GREEN=$'\033[32m'
C_YELLOW=$'\033[33m'
C_RED=$'\033[31m'
C_MAG=$'\033[35m'
C_DIM=$'\033[2m'

color_pct() {
  local p=${1%.*}
  if [ "$p" -ge 85 ]; then printf '%s' "$C_RED"
  elif [ "$p" -ge 60 ]; then printf '%s' "$C_YELLOW"
  else printf '%s' "$C_GREEN"; fi
}

SEP="${C_DIM} | ${C_RESET}"
out=""
add() { if [ -z "$out" ]; then out="$1"; else out="$out$SEP$1"; fi; }

# Directory (shorten $HOME to ~)
if [ -n "$cwd" ]; then
  short="${cwd/#$HOME/~}"
  add "${C_BLUE}${short}${C_RESET}"
fi

# Git (skip optional locks)
if [ -n "$cwd" ] && GIT="git -C $cwd --no-optional-locks" && $GIT rev-parse --git-dir >/dev/null 2>&1; then
  branch=$($GIT symbolic-ref --short HEAD 2>/dev/null || $GIT rev-parse --short HEAD 2>/dev/null)
  porcelain=$($GIT status --porcelain 2>/dev/null)
  staged=$(echo "$porcelain" | grep -c '^[MADRC]')
  modified=$(echo "$porcelain" | grep -c '^.[MD]')
  untracked=$(echo "$porcelain" | grep -c '^??')
  g="${C_CYAN} ${branch}${C_RESET}"
  [ "$staged" -gt 0 ] && g="$g ${C_GREEN}+${staged}${C_RESET}"
  [ "$modified" -gt 0 ] && g="$g ${C_YELLOW}~${modified}${C_RESET}"
  [ "$untracked" -gt 0 ] && g="$g ${C_RED}?${untracked}${C_RESET}"
  if [ -z "$porcelain" ]; then g="$g ${C_GREEN}clean${C_RESET}"; fi
  counts=$($GIT rev-list --left-right --count '@{upstream}...HEAD' 2>/dev/null)
  if [ -n "$counts" ]; then
    behind=${counts%%[[:space:]]*}
    ahead=${counts##*[[:space:]]}
    [ "$ahead" -gt 0 ] && g="$g ${C_GREEN}↑${ahead}${C_RESET}"
    [ "$behind" -gt 0 ] && g="$g ${C_RED}↓${behind}${C_RESET}"
  fi
  add "$g"
fi

[ -n "$wt" ] && add "${C_MAG}wt:${wt}${C_RESET}"

if [ -n "$pr" ]; then
  if [ "$pr_kind" = "mr" ]; then label="MR !$pr"; else label="PR #$pr"; fi
  add "${C_MAG}${label}${pr_state:+ ($pr_state)}${C_RESET}"
fi

# Model (+ effort)
if [ -n "$model" ]; then
  add "${model}${effort:+ ${C_DIM}[${effort}]${C_RESET}}"
fi

# Context
if [ -n "$used" ]; then
  add "ctx $(color_pct "$used")$(printf '%.0f' "$used")%${C_RESET}"
fi

# Cost + duration
if [ -n "$cost" ]; then
  c="\$$(printf '%.2f' "$cost")"
  if [ -n "$dur_ms" ]; then
    mins=$((${dur_ms%.*} / 60000))
    c="$c ${C_DIM}${mins}m${C_RESET}"
  fi
  add "$c"
fi

# Lines changed
if [ -n "$added$removed" ] && { [ "${added:-0}" -gt 0 ] || [ "${removed:-0}" -gt 0 ]; }; then
  add "${C_GREEN}+${added:-0}${C_RESET}/${C_RED}-${removed:-0}${C_RESET}"
fi

# Rate limits
rl=""
[ -n "$five" ] && rl="5h $(color_pct "$five")$(printf '%.0f' "$five")%${C_RESET}"
[ -n "$week" ] && rl="${rl:+$rl }7d $(color_pct "$week")$(printf '%.0f' "$week")%${C_RESET}"
[ -n "$rl" ] && add "$rl"

[ -n "$cold" ] && add "${C_YELLOW}cache cold${C_RESET}"
[ -n "$vim" ] && add "${C_DIM}${vim}${C_RESET}"

printf '%s\n' "$out"
