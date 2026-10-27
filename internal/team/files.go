package team

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// fileSnapshot is a worktree's dirty paths in porcelain order. The state carries
// the porcelain code plus a content fingerprint: a file already dirty before
// the task is still changed if the worker edits it again.
type fileSnapshot struct {
	states map[string]string
	order  []string
}

type statusEntry struct {
	path string
	code string
}

// gitSnapshot snapshots the worktree, reporting false when git cannot be used.
// Attribution is best-effort: no git binary, no repository and a failed status
// all leave git out of the result, because a missing diff must never fail a
// task.
func gitSnapshot(dir string) (fileSnapshot, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return fileSnapshot{}, false
	}
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = dir
	// status only refreshes the index when it is allowed to; a guarded
	// container mounts .git read-only and must not turn a status into a write.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return fileSnapshot{}, false
	}
	entries := parseStatusOrder(out)
	states := make(map[string]string, len(entries))
	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		states[entry.path] = entry.code + "\x00" + fingerprint(dir, entry.path)
		order = append(order, entry.path)
	}
	return fileSnapshot{states: states, order: order}, true
}

// gitChanged keeps the map shape for callers that only need the porcelain
// status; the content fingerprint rides along in the value.
func gitChanged(dir string) (map[string]string, error) {
	snap, ok := gitSnapshot(dir)
	if !ok {
		return map[string]string{}, nil
	}
	return snap.states, nil
}

// fingerprint identifies a dirty file by size and mtime. A status comparison
// alone cannot see a second edit to a path that was already dirty.
func fingerprint(dir, p string) string {
	info, err := os.Lstat(filepath.Join(dir, p))
	if err != nil {
		return ""
	}
	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

func parseStatus(out []byte) map[string]string {
	entries := parseStatusOrder(out)
	status := make(map[string]string, len(entries))
	for _, entry := range entries {
		status[entry.path] = entry.code
	}
	return status
}

func parseStatusOrder(out []byte) []statusEntry {
	var entries []statusEntry
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		code, rest := line[:2], line[3:]
		// A rename or copy is one line carrying both paths; the old one left
		// the tree and the new one entered it.
		if (code[0] == 'R' || code[0] == 'C') && strings.Contains(rest, " -> ") {
			old, new, _ := strings.Cut(rest, " -> ")
			entries = append(entries,
				statusEntry{path: unquotePath(old), code: code},
				statusEntry{path: unquotePath(new), code: code})
			continue
		}
		entries = append(entries, statusEntry{path: unquotePath(rest), code: code})
	}
	return entries
}

// unquotePath undoes git's C-style quoting, which core.quotePath applies to
// paths with spaces, tabs or non-ASCII bytes.
func unquotePath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if unquoted, err := strconv.Unquote(p); err == nil {
			return unquoted
		}
	}
	return p
}

// changedPaths lists every path whose state differs between two snapshots,
// including paths that disappeared from the second one. Porcelain order is
// kept so the result is stable without sorting.
func changedPaths(before, after fileSnapshot) []string {
	changed := make([]string, 0)
	seen := make(map[string]struct{}, len(after.order))
	for _, p := range after.order {
		seen[p] = struct{}{}
		if old, ok := before.states[p]; !ok || old != after.states[p] {
			changed = append(changed, p)
		}
	}
	for _, p := range before.order {
		if _, ok := after.states[p]; !ok {
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			changed = append(changed, p)
		}
	}
	return changed
}

// streamPaths normalises the worker's edit events to the repo-relative shape
// git porcelain reports, so the two attributions can be deduplicated.
func streamPaths(cwd string, edited []string) []string {
	out := make([]string, 0, len(edited))
	for _, p := range edited {
		if p == "" {
			continue
		}
		if cwd != "" && filepath.IsAbs(p) {
			if rel, err := filepath.Rel(cwd, p); err == nil && rel != "." && rel != ".." &&
				!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				p = rel
			}
		}
		out = append(out, filepath.ToSlash(filepath.Clean(p)))
	}
	return out
}

// unionPaths keeps git's paths first, in porcelain order, and appends only the
// stream's edits that git did not attribute, so a partial diff cannot hide one.
func unionPaths(git, stream []string) []string {
	out := make([]string, 0, len(git)+len(stream))
	seen := make(map[string]struct{}, len(git))
	for _, p := range git {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range stream {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
