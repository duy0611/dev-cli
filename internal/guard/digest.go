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
	// Sum is stored on the row and compared at rebuild.
	Sum string
	// Fields and Files name what changed in a mismatch. Each holds hashes, not
	// values.
	Fields map[string]string
	Files  []string
	files  map[string]string
}

// record is the stored form of a Digest's hashes.
type record struct {
	Fields map[string]string `json:"fields"`
	Files  map[string]string `json:"files"`
}

// Record encodes the hashes for the row.
func (d Digest) Record() string {
	b, _ := json.Marshal(record{Fields: d.Fields, Files: d.files})
	return string(b)
}

// ParseRecord reads what Record wrote. An unreadable record yields an empty
// Digest, which Diff reports as all new.
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

// volatile are merged fields that change with no change to what runs.
// configFilePath is a fresh temporary directory on every invocation.
var volatile = []string{"configFilePath"}

// DigestOf computes the digest of a merged configuration, with the Dockerfile
// and compose files it names, since those are what run.
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
			// A missing file is an error: a digest that skipped it would match
			// one that has it.
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

// WithoutMounts returns merged with the mounts at targets removed, in either
// CLI spelling. Used for mounts that follow the host's git config, which must
// not count as a changed configuration.
func WithoutMounts(merged []byte, targets []string) ([]byte, error) {
	if len(targets) == 0 {
		return merged, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(merged, &doc); err != nil {
		return nil, fmt.Errorf("reading the merged configuration: %w", err)
	}
	raw, ok := doc["mounts"]
	if !ok {
		return merged, nil
	}
	var mounts []json.RawMessage
	if err := json.Unmarshal(raw, &mounts); err != nil {
		return nil, fmt.Errorf("reading the merged configuration's mounts: %w", err)
	}
	kept := mounts[:0]
	for _, m := range mounts {
		parsed, err := parseMount(m)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(targets, parsed.target) {
			kept = append(kept, m)
		}
	}
	if len(kept) == len(mounts) {
		return merged, nil
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	doc["mounts"] = b
	return json.Marshal(doc)
}

// Diff names the fields and files that differ between two digests, sorted.
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

// canonical re-encodes JSON with sorted keys, so key order and whitespace do
// not matter.
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
