package dcgen

import (
	"encoding/json"
	"strings"
	"testing"
)

// mountsOfJSON pulls the "mounts" array out of a rendered document as strings,
// for assertions. An object-form entry comes back as its compact JSON.
func mountsOfJSON(t *testing.T, doc []byte) []string {
	t.Helper()
	var parsed struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, doc)
	}
	out := make([]string, 0, len(parsed.Mounts))
	for _, raw := range parsed.Mounts {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			out = append(out, s)
			continue
		}
		out = append(out, string(raw))
	}
	return out
}

// The ordinary case: a project that never heard of dev gets dev's mount added
// and everything else back unchanged.
func TestOverlayAddsTheStateMount(t *testing.T) {
	in := []byte(`{
	  "name": "demo",
	  "image": "ubuntu",
	  "features": {"ghcr.io/devcontainers/features/go:1": {}}
	}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	want := "source=dev-ws-c1-state,target=/var/dev-state,type=volume"
	if got := mountsOfJSON(t, out); len(got) != 1 || got[0] != want {
		t.Errorf("mounts = %v, want [%s]", got, want)
	}

	// Every field dev does not touch must survive. --override-config replaces
	// rather than deep-merges, so a field dropped here is a field the container
	// loses.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"name", "image", "features"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("Overlay dropped %q: %s", key, out)
		}
	}
}

// A project's own mounts at other targets are none of dev's business.
func TestOverlayPreservesOtherMounts(t *testing.T) {
	in := []byte(`{"mounts": ["source=cache,target=/cache,type=volume"]}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	got := mountsOfJSON(t, out)
	if len(got) != 2 {
		t.Fatalf("mounts = %v, want 2 entries", got)
	}
	if got[0] != "source=cache,target=/cache,type=volume" {
		t.Errorf("project mount not preserved first: %v", got)
	}
	if !strings.Contains(got[1], "dev-ws-c1-state") {
		t.Errorf("dev mount not appended last: %v", got)
	}
}

// The replace rule, string spelling. Leaving both would make docker refuse the
// whole run with "duplicate mount destination" — the failure this change exists
// to remove.
func TestOverlayReplacesAStringStateMount(t *testing.T) {
	in := []byte(`{"mounts": ["source=dev-cli-state,target=/var/dev-state,type=volume"]}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	got := mountsOfJSON(t, out)
	if len(got) != 1 {
		t.Fatalf("mounts = %v, want exactly 1 entry", got)
	}
	if strings.Contains(got[0], "dev-cli-state") {
		t.Errorf("project's state mount survived: %v", got)
	}
	if !strings.Contains(got[0], "dev-ws-c1-state") {
		t.Errorf("dev's state mount missing: %v", got)
	}
}

// The replace rule, object spelling. The CLI accepts both, so target detection
// must read both or an object-form project doubles the mount.
func TestOverlayReplacesAnObjectStateMount(t *testing.T) {
	in := []byte(`{"mounts": [{"source": "dev-cli-state", "target": "/var/dev-state", "type": "volume"}]}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	got := mountsOfJSON(t, out)
	if len(got) != 1 {
		t.Fatalf("mounts = %v, want exactly 1 entry", got)
	}
	if strings.Contains(got[0], "dev-cli-state") {
		t.Errorf("project's state mount survived: %v", got)
	}
}

// devcontainer.json is JSONC. VS Code's own templates ship comments, and
// encoding/json rejects both of these.
func TestOverlayAcceptsJSONC(t *testing.T) {
	in := []byte(`// what this container is
{
  "name": "demo", // trailing comment
  "mounts": [
    "source=cache,target=/cache,type=volume",
  ],
}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}
	if len(mountsOfJSON(t, out)) != 2 {
		t.Errorf("mounts = %v, want 2", mountsOfJSON(t, out))
	}
}

// The case a hand-rolled comment stripper gets wrong. Corrupting this line
// would change what the container runs, silently, on every up.
func TestOverlayKeepsCommentMarkersInsideStrings(t *testing.T) {
	in := []byte(`{"postCreateCommand": "echo // not a comment"}`)

	out, err := Overlay(in, State{Volume: "dev-ws-c1-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	var parsed struct {
		PostCreate string `json:"postCreateCommand"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.PostCreate != "echo // not a comment" {
		t.Errorf("postCreateCommand = %q, want the string intact", parsed.PostCreate)
	}
}

// dev parses a file it did not write, so it must report a file it cannot parse
// rather than return a partial document.
func TestOverlayRejectsAMalformedDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"malformed JSON", `{"name":}`},
		{"null", `null`},
		{"array", `[]`},
		{"string", `"not an object"`},
		{"number", `42`},
		{"boolean", `true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Overlay([]byte(tc.in), State{Volume: "v"})
			if err == nil {
				t.Errorf("Overlay accepted %s", tc.name)
			}
		})
	}
}

// A project-owned worktree container needs both of a worktree's bind mounts,
// merged the same way the state mount is: dev never writes into a project's
// own devcontainer.json (invariant 9), so a generated document is not an
// option here.
func TestOverlayWorktreeAddsBothMounts(t *testing.T) {
	in := []byte(`{"name": "demo", "image": "ubuntu"}`)

	out, err := OverlayWorktree(in, "/home/u/src/app/.git", "/home/u/wt/feat")
	if err != nil {
		t.Fatalf("OverlayWorktree: %v", err)
	}

	got := mountsOfJSON(t, out)
	want := []string{
		"source=/home/u/src/app/.git,target=/home/u/src/app/.git,type=bind",
		"source=/home/u/wt/feat,target=/home/u/wt/feat,type=bind",
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("mounts = %v, want %v", got, want)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"name", "image"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("OverlayWorktree dropped %q: %s", key, out)
		}
	}
}

// The project's own mounts at other targets survive, appended after.
func TestOverlayWorktreePreservesOtherMounts(t *testing.T) {
	in := []byte(`{"mounts": ["source=cache,target=/cache,type=volume"]}`)

	out, err := OverlayWorktree(in, "/repo/.git", "/wt/feat")
	if err != nil {
		t.Fatalf("OverlayWorktree: %v", err)
	}
	got := mountsOfJSON(t, out)
	if len(got) != 3 {
		t.Fatalf("mounts = %v, want 3 entries", got)
	}
	if got[0] != "source=cache,target=/cache,type=volume" {
		t.Errorf("project mount not preserved first: %v", got)
	}
}

// The replace rule, same as the state mount: a project that already names a
// mount at one of the two paths gets it replaced, not duplicated — docker
// refuses "duplicate mount destination" otherwise.
func TestOverlayWorktreeReplacesACollidingMount(t *testing.T) {
	in := []byte(`{"mounts": ["source=x,target=/repo/.git,type=bind"]}`)

	out, err := OverlayWorktree(in, "/repo/.git", "/wt/feat")
	if err != nil {
		t.Fatalf("OverlayWorktree: %v", err)
	}
	got := mountsOfJSON(t, out)
	if len(got) != 2 {
		t.Fatalf("mounts = %v, want exactly 2 entries", got)
	}
	for _, m := range got {
		if strings.Contains(m, "source=x,") {
			t.Errorf("project's colliding mount survived: %v", got)
		}
	}
}

// A worktree container that also persists state needs both merges applied:
// dev's overlay runs once per invocation, and the state mount must not be
// dropped by the worktree merge or vice versa.
func TestOverlayWorktreeThenStateKeepsBothMounts(t *testing.T) {
	in := []byte(`{"name": "demo"}`)

	mid, err := OverlayWorktree(in, "/repo/.git", "/wt/feat")
	if err != nil {
		t.Fatalf("OverlayWorktree: %v", err)
	}
	out, err := Overlay(mid, State{Volume: "dev-ws-feat-state"})
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}

	got := mountsOfJSON(t, out)
	if len(got) != 3 {
		t.Fatalf("mounts = %v, want 3 entries", got)
	}
}

// A compose project cannot carry the mount, so create warns. Detection is free
// here because the document is already parsed.
func TestUsesCompose(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"compose", `{"dockerComposeFile": "docker-compose.yml", "service": "app"}`, true},
		{"compose list", `{"dockerComposeFile": ["a.yml", "b.yml"]}`, true},
		{"image", `{"image": "ubuntu"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UsesCompose([]byte(tc.in))
			if err != nil {
				t.Fatalf("UsesCompose: %v", err)
			}
			if got != tc.want {
				t.Errorf("UsesCompose = %v, want %v", got, tc.want)
			}
		})
	}
}
