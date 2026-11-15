// Package acp bridges an Agent Client Protocol client on the host to an agent
// running inside a container, rewriting the newline-delimited JSON-RPC stream
// between them.
package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Root pairs a host folder with where it appears inside the container.
type Root struct {
	Host      string
	Container string
}

// Bridge rewrites the newline-delimited JSON-RPC stream between a host client
// and a container agent.
type Bridge struct {
	// Roots map host paths to container paths, longest first after New.
	Roots []Root
	// Cwd is the container directory a session starts in when the client asks
	// for a cwd no root maps, as Buzz does with its own working directory.
	Cwd string
	// Log receives one line per rewrite worth knowing about; Trace, when set,
	// every message in both directions.
	Log   io.Writer
	Trace io.Writer

	traceMu sync.Mutex
}

// New returns a bridge over roots, dropping identity and empty pairs.
func New(roots []Root, cwd string, log io.Writer) *Bridge {
	var kept []Root
	for _, r := range roots {
		r.Host = strings.TrimRight(r.Host, "/")
		r.Container = strings.TrimRight(r.Container, "/")
		if r.Host == "" || r.Container == "" || r.Host == r.Container {
			continue
		}
		kept = append(kept, r)
	}
	sort.SliceStable(kept, func(i, j int) bool { return len(kept[i].Host) > len(kept[j].Host) })
	return &Bridge{Roots: kept, Cwd: cwd, Log: log}
}

// ToAgent copies client messages to the agent until src ends.
func (b *Bridge) ToAgent(dst io.Writer, src io.Reader) error {
	return b.pump(dst, src, "->", b.fromClient)
}

// ToClient copies agent messages to the client until src ends.
func (b *Bridge) ToClient(dst io.Writer, src io.Reader) error {
	return b.pump(dst, src, "<-", b.fromAgent)
}

func (b *Bridge) pump(dst io.Writer, src io.Reader, dir string, rewrite func(map[string]any)) error {
	r := bufio.NewReader(src)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			out := b.rewriteLine(line, rewrite)
			b.trace(dir, out)
			if _, werr := dst.Write(out); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// rewriteLine passes anything that is not a JSON object through untouched: a
// stray log line on the agent's stdout is the agent's bug, not the bridge's.
func (b *Bridge) rewriteLine(line []byte, rewrite func(map[string]any)) []byte {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var msg map[string]any
	if err := dec.Decode(&msg); err != nil || msg == nil {
		return line
	}
	if _, err := dec.Token(); err != io.EOF {
		return line
	}
	rewrite(msg)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(msg); err != nil {
		return line
	}
	return buf.Bytes()
}

func (b *Bridge) fromClient(msg map[string]any) {
	params, _ := msg["params"].(map[string]any)
	switch msg["method"] {
	case "initialize":
		// The client's fs and terminal are the host's; the agent must use the
		// container's own. Deleted, not set false: an omitted capability is
		// unsupported in every protocol version, and the v2 drafts disagree on
		// the shape of a present one.
		if params != nil {
			if caps, ok := params["clientCapabilities"].(map[string]any); ok {
				delete(caps, "fs")
				delete(caps, "terminal")
				if auth, ok := caps["auth"].(map[string]any); ok {
					delete(auth, "terminal")
				}
			}
		}
	case "session/new", "session/load", "session/resume":
		if params != nil {
			b.sessionParams(params)
			// Already mapped, with their own drop and fallback rules; a second
			// pass would chain through the roots.
			held := map[string]any{}
			for _, k := range []string{"cwd", "additionalDirectories"} {
				if v, ok := params[k]; ok {
					held[k] = v
					delete(params, k)
				}
			}
			walk(msg, b.toContainer)
			for k, v := range held {
				params[k] = v
			}
			return
		}
	}
	walk(msg, b.toContainer)
}

func (b *Bridge) sessionParams(params map[string]any) {
	if cwd, ok := params["cwd"].(string); ok {
		if mapped, ok := b.mapPath(cwd); ok {
			params["cwd"] = mapped
		} else if b.Cwd != "" {
			b.logf("acp: cwd %s is not a folder of this container; using %s", cwd, b.Cwd)
			params["cwd"] = b.Cwd
		}
	}
	if servers, ok := params["mcpServers"].([]any); ok && len(servers) > 0 {
		b.logf("acp: dropping %d MCP server(s) from the client: they run on the host", len(servers))
		params["mcpServers"] = []any{}
	}
	if dirs, ok := params["additionalDirectories"].([]any); ok {
		kept := dirs[:0]
		for _, d := range dirs {
			s, _ := d.(string)
			if mapped, ok := b.mapPath(s); ok {
				kept = append(kept, mapped)
			} else {
				b.logf("acp: dropping additional directory %s: not a folder of this container", s)
			}
		}
		params["additionalDirectories"] = kept
	}
}

func (b *Bridge) fromAgent(msg map[string]any) {
	// Longest container path first, so a root nested in another wins.
	roots := slices.Clone(b.Roots)
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i].Container) > len(roots[j].Container) })
	walk(msg, func(s string) string {
		return rewritePaths(s, roots, func(r Root) (string, string) { return r.Container, r.Host })
	})
}

// mapPath maps a host path that is, or is under, a root.
func (b *Bridge) mapPath(p string) (string, bool) {
	for _, r := range b.Roots {
		if p == r.Host {
			return r.Container, true
		}
		if strings.HasPrefix(p, r.Host+"/") {
			return r.Container + p[len(r.Host):], true
		}
	}
	return "", false
}

func (b *Bridge) toContainer(s string) string {
	return rewritePaths(s, b.Roots, func(r Root) (string, string) { return r.Host, r.Container })
}

// rewritePaths replaces each root's from wherever it appears as a whole path,
// inside prose and file:// URIs alike: /workspaces/app must not touch
// /workspaces/apple or /other/workspaces/app. One pass, so a replacement is
// never matched again by a later root.
func rewritePaths(s string, roots []Root, pair func(Root) (from, to string)) string {
	var out strings.Builder
	last := 0
	for i := 0; i < len(s); {
		hit := false
		for _, r := range roots {
			from, to := pair(r)
			if !strings.HasPrefix(s[i:], from) || !wholePath(s, i, i+len(from)) {
				continue
			}
			out.WriteString(s[last:i])
			out.WriteString(to)
			i += len(from)
			last = i
			hit = true
			break
		}
		if !hit {
			i++
		}
	}
	if last == 0 {
		return s
	}
	out.WriteString(s[last:])
	return out.String()
}

// wholePath reports whether s[start:end] is bounded by something other than
// the middle of a name. A trailing dot ends a sentence unless a name follows.
func wholePath(s string, start, end int) bool {
	if start > 0 && isNameByte(s[start-1]) {
		return false
	}
	if end == len(s) || !isNameByte(s[end]) {
		return true
	}
	return s[end] == '.' && (end+1 == len(s) || !isNameByte(s[end+1]))
}

func isNameByte(c byte) bool {
	return c == '-' || c == '_' || c == '.' ||
		('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// walk applies f to every string value in v, in place.
func walk(v any, f func(string) string) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = walk(e, f)
		}
	case []any:
		for i, e := range t {
			t[i] = walk(e, f)
		}
	case string:
		return f(t)
	}
	return v
}

func (b *Bridge) logf(format string, args ...any) {
	if b.Log != nil {
		fmt.Fprintf(b.Log, format+"\n", args...)
	}
}

func (b *Bridge) trace(dir string, line []byte) {
	if b.Trace == nil {
		return
	}
	b.traceMu.Lock()
	defer b.traceMu.Unlock()
	fmt.Fprintf(b.Trace, "%s %s", dir, line)
}
