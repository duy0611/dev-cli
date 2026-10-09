package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// Digest identifies the configuration a container is built from.
type Digest struct {
	// Sum is what is stored on the row and compared at rebuild.
	Sum string
	// Fields and Files are what went into it, so a mismatch can say what
	// changed rather than only that something did. Each is held as a hash of
	// its canonical value, never the value itself, so the record stored for a
	// later comparison (Record) says which field changed without keeping a copy
	// of a document dev does not own.
	Fields map[string]string
	Files  []string
	files  map[string]string
}

// record is what Record stores and ParseRecord reads: one hash per field and
// per file.
type record struct {
	Fields map[string]string `json:"fields"`
	Files  map[string]string `json:"files"`
}

// Record encodes what went into the digest, for the row, so a later rebuild can
// say which fields changed. Hashes only.
func (d Digest) Record() string {
	b, _ := json.Marshal(record{Fields: d.Fields, Files: d.files})
	return string(b)
}

// ParseRecord reads what Record wrote. An empty or unreadable record yields an
// empty Digest, which Diff compares as "everything is new" — the refusal still
// happens, on Sum; only the list of what changed is less specific.
func ParseRecord(sum, s string) Digest {
	var r record
	_ = json.Unmarshal([]byte(s), &r)
	d := Digest{Sum: sum, Fields: r.Fields, files: r.Files}
	if d.Fields == nil {
		d.Fields = map[string]string{}
	}
	if d.files == nil {
		d.files = map[string]string{}
	}
	d.Files = slices.Sorted(maps.Keys(d.files))
	return d
}

// volatile are the merged fields that change with no change to what runs.
// configFilePath is where the CLI read the document: a fresh temporary
// directory on every invocation of a generated or merged configuration.
var volatile = []string{"configFilePath"}

// DigestOf computes the digest of a merged configuration, together with the
// Dockerfile it builds from and the compose files it names.
//
// The merged configuration rather than the project's file bytes, because what
// matters is what will run: a feature's metadata changes it without the file
// changing, and whitespace in the file changes the bytes without changing it.
// The Dockerfile and compose files are read separately because the merged
// configuration names them rather than containing them — and a RUN line or a
// privileged service is as much what runs as initializeCommand is.
//
// configPath is the configuration file, which a relative build.dockerfile is
// resolved against, as the CLI resolves it.
func DigestOf(merged []byte, configPath string, composeFiles []string) (Digest, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(merged, &doc); err != nil {
		return Digest{}, fmt.Errorf("reading the merged configuration: %w", err)
	}
	for _, k := range volatile {
		delete(doc, k)
	}

	d := Digest{Fields: map[string]string{}, files: map[string]string{}}
	for k, v := range doc {
		canon, err := canonical(v)
		if err != nil {
			return Digest{}, fmt.Errorf("reading the merged configuration's %s: %w", k, err)
		}
		sum := sha256.Sum256([]byte(canon))
		d.Fields[k] = hex.EncodeToString(sum[:])
	}

	paths := slices.Clone(composeFiles)
	if dockerfile := dockerfileOf(doc); dockerfile != "" {
		if !filepath.IsAbs(dockerfile) {
			dockerfile = filepath.Join(filepath.Dir(configPath), dockerfile)
		}
		paths = append(paths, dockerfile)
	}
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			// An error, not an absent file: a configuration that builds from a
			// Dockerfile which is not there will fail at build anyway, and a
			// digest that skipped it would match a later one that has it.
			return Digest{}, fmt.Errorf("reading %s for the configuration digest: %w", p, err)
		}
		name := filepath.Base(p)
		sum := sha256.Sum256(content)
		d.files[name] = hex.EncodeToString(sum[:])
		d.Files = append(d.Files, name)
	}
	slices.Sort(d.Files)

	h := sha256.New()
	for _, k := range slices.Sorted(maps.Keys(d.Fields)) {
		fmt.Fprintf(h, "field %q %s\n", k, d.Fields[k])
	}
	for _, name := range d.Files {
		fmt.Fprintf(h, "file %q %s\n", name, d.files[name])
	}
	d.Sum = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return d, nil
}

// Diff names what differs between two digests: the top-level fields of the
// merged configuration that changed, appeared or went, and the files whose
// content changed. Sorted, so the list reads the same every time.
func Diff(a, b Digest) []string {
	var out []string
	for _, k := range sortedUnion(slices.Collect(maps.Keys(a.Fields)), slices.Collect(maps.Keys(b.Fields))) {
		if a.Fields[k] != b.Fields[k] {
			out = append(out, k)
		}
	}
	for _, k := range sortedUnion(slices.Collect(maps.Keys(a.files)), slices.Collect(maps.Keys(b.files))) {
		if a.files[k] != b.files[k] {
			out = append(out, k)
		}
	}
	return out
}

func sortedUnion(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// canonical re-encodes a JSON value with object keys sorted, which is what
// encoding/json does for a map — so two documents that differ only in key
// order or whitespace have the same encoding.
func canonical(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// dockerfileOf returns build.dockerfile, or the legacy top-level dockerFile.
func dockerfileOf(doc map[string]json.RawMessage) string {
	var build struct {
		Dockerfile string `json:"dockerfile"`
	}
	if raw, ok := doc["build"]; ok && json.Unmarshal(raw, &build) == nil && build.Dockerfile != "" {
		return build.Dockerfile
	}
	var legacy string
	if raw, ok := doc["dockerFile"]; ok && json.Unmarshal(raw, &legacy) == nil {
		return legacy
	}
	return ""
}
