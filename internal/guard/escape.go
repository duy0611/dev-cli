// Package guard decides whether a container's configuration reaches past it.
//
// It reads; it never runs anything. The devcontainer CLI does the reading of
// the project — the merged configuration it reports is the input here — so
// this package holds only the judgement, where a test can reach every branch
// without an engine.
package guard

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Finding is one thing a configuration asks for that reaches the host.
type Finding struct {
	// What it asks for, as the operator would search for it.
	What string
	// From is the field of the merged configuration it was found in. A
	// feature's privileged has already been OR'd into "privileged" by the CLI,
	// so that field covers the project and every feature at once.
	From string
}

func (f Finding) String() string { return f.What + " (" + f.From + ")" }

// Escape returns everything in a merged configuration that gives the container
// a way to the host: privileged mode, the engine's socket, a host namespace, a
// device, or a bind mount of the host's root or the operator's home.
//
// Merged, because an override document cannot remove what a feature adds —
// docker-in-docker declares privileged itself — so the only place to see the
// whole of what will run is after the CLI has combined it.
//
// Not here, deliberately: capAdd, securityOpt and init. They widen what root
// can do inside the container, not the way out of it, and the Go feature in
// dev's own catalog asks for SYS_PTRACE and seccomp=unconfined so a debugger
// works.
//
// home is the operator's home directory, resolved, so a mount of it or of any
// directory above it is caught: on Docker Desktop and OrbStack /Users is shared
// into the engine's VM, and such a mount reaches ~/.ssh and every credential
// on the machine.
func Escape(merged []byte, home string) ([]Finding, error) {
	var doc struct {
		Privileged bool              `json:"privileged"`
		Mounts     []json.RawMessage `json:"mounts"`
		RunArgs    []string          `json:"runArgs"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		return nil, fmt.Errorf("reading the merged configuration: %w", err)
	}

	var out findings
	if doc.Privileged {
		out.add("privileged: true", "privileged")
	}
	for _, raw := range doc.Mounts {
		m, err := parseMount(raw)
		if err != nil {
			return nil, err
		}
		if what := hostMount(m, home); what != "" {
			out.add(what, "mounts")
		}
	}
	for _, f := range runArgs(doc.RunArgs, home) {
		out.add(f, "runArgs")
	}
	return out.list, nil
}

// findings keeps the first of each identical finding, in order: the same route
// asked for twice in one place needs changing once, while the same route in two
// places is two things for the operator to change.
type findings struct {
	list []Finding
	seen map[Finding]bool
}

func (f *findings) add(what, from string) {
	k := Finding{What: what, From: from}
	if f.seen == nil {
		f.seen = map[Finding]bool{}
	}
	if !f.seen[k] {
		f.seen[k] = true
		f.list = append(f.list, k)
	}
}

// mount is the part of a mounts entry that matters here.
type mount struct {
	kind, source, target string
}

// parseMount reads either spelling the CLI accepts: the string
// "source=x,target=/y,type=bind" or the object {"source": "x", ...}.
func parseMount(raw json.RawMessage) (mount, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseMountString(s), nil
	}
	var obj struct {
		Type   string `json:"type"`
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return mount{}, fmt.Errorf("reading a mounts entry %s: %w", raw, err)
	}
	return mount{kind: obj.Type, source: obj.Source, target: obj.Target}, nil
}

// parseMountString reads docker's --mount syntax, which is also
// devcontainer.json's string form: comma-separated key=value, with src and
// source the same key.
func parseMountString(s string) mount {
	var m mount
	for field := range strings.SplitSeq(s, ",") {
		k, v, _ := strings.Cut(field, "=")
		switch strings.TrimSpace(k) {
		case "type":
			m.kind = v
		case "source", "src":
			m.source = v
		case "target", "dst", "destination":
			m.target = v
		}
	}
	return m
}

// hostMount names what a mount reaches on the host, or "" when it reaches
// nothing that matters.
//
// Only a bind mount reaches a host path. docker's --mount defaults the type to
// volume, so an entry with no type is a named volume — whose name could be
// anything, including "docker.sock", without touching the host.
func hostMount(m mount, home string) string {
	if m.kind != "bind" || m.source == "" {
		return ""
	}
	return hostPath(m.source, home)
}

// hostPath names what a bind-mounted host path reaches, or "".
func hostPath(src, home string) string {
	clean := path.Clean(src)
	switch base := path.Base(clean); base {
	case "docker.sock", "podman.sock":
		return "the engine socket " + clean + " (" + base + ")"
	}
	if strings.HasPrefix(clean, "/run/podman/") || strings.HasPrefix(clean, "/var/run/podman/") {
		return "the engine socket " + clean + " (podman.sock)"
	}
	if clean == "/" || (home != "" && isAncestorOrSelf(clean, path.Clean(home))) {
		return "the host filesystem at " + clean
	}
	return ""
}

// isAncestorOrSelf reports whether dir is p or a directory above it.
// /Users/mel is not above /Users/me: the comparison is by path segment.
func isAncestorOrSelf(dir, p string) bool {
	if dir == p {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// hostNamespaces are the runArgs that join one of the host's namespaces. Each
// takes its value either after "=" or as the next argument.
var hostNamespaces = map[string]string{
	"--pid":     "--pid=host",
	"--network": "--network=host",
	"--net":     "--network=host",
	"--ipc":     "--ipc=host",
	"--userns":  "--userns=host",
	"--uts":     "--uts=host",
}

// runArgs finds what docker run arguments ask of the host. docker accepts a
// flag's value joined with "=" or as the next argument, so both are read.
func runArgs(args []string, home string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		flag, value, joined := strings.Cut(args[i], "=")
		next := func() string {
			if joined {
				return value
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}

		switch flag {
		case "--privileged":
			// A bare flag; --privileged=false is the one value that is not.
			if !joined || value != "false" {
				out = append(out, "privileged: true")
			}
		case "--device":
			out = append(out, "--device "+next())
		case "-v", "--volume":
			// host:container[:opts]. A source without a leading slash is a
			// named volume, not a host path.
			if src, _, _ := strings.Cut(next(), ":"); strings.HasPrefix(src, "/") {
				if what := hostPath(src, home); what != "" {
					out = append(out, what)
				}
			}
		case "--mount":
			if what := hostMount(parseMountString(next()), home); what != "" {
				out = append(out, what)
			}
		default:
			if canonical, ok := hostNamespaces[flag]; ok && next() == "host" {
				out = append(out, canonical)
			}
		}
	}
	return out
}
