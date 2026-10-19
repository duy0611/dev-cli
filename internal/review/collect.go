package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Folder is one host folder of a guarded container. Label is empty when the
// container has a single folder.
type Folder struct {
	Label string
	Path  string
}

// UntrackedLimit is the size above which an untracked file is noted rather
// than shown, so a stray build artifact does not swamp the review file.
const UntrackedLimit = 256 << 10

// emptyTree is git's hash of a tree with no entries: what HEAD means before
// the repository has a commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// gitSafeArgs force off settings a container could have tampered with, and
// keep paths unquoted so non-ASCII names survive.
var gitSafeArgs = []string{"-c", "core.fsmonitor=false", "-c", "core.quotepath=false"}

// diffSafeArgs keep a project's attributes from selecting a command or
// textconv program, and pin the prefixes the parser reads.
var diffSafeArgs = []string{
	"--no-ext-diff", "--no-textconv", "--no-color",
	"--no-renames", "--src-prefix=a/", "--dst-prefix=b/",
}

// Collect reads each folder's uncommitted changes with host git. A folder that
// is not a git repository is reported in Diff.Skipped instead of failing, since
// one extra folder should not block review of the rest.
func Collect(ctx context.Context, folders []Folder) (Diff, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Diff{}, fmt.Errorf("host git is required to read a guarded container's changes: %w", err)
	}

	var d Diff
	for _, folder := range folders {
		files, skipped, err := collectFolder(ctx, folder)
		if err != nil {
			return Diff{}, err
		}
		if skipped {
			d.Skipped = append(d.Skipped, folder.Path)
			continue
		}
		d.Files = append(d.Files, files...)
	}
	return d, nil
}

func collectFolder(ctx context.Context, folder Folder) ([]File, bool, error) {
	g := newGitRunner(ctx, folder.Path)
	inside, err := g.run(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return nil, true, nil
	}

	target := emptyTree
	if head, err := g.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		if h := strings.TrimSpace(head); h != "" {
			target = h
		}
	}

	args := append([]string{"diff", target}, diffSafeArgs...)
	args = append(args, "--relative")
	text, err := g.run(ctx, args...)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s's changes: %w", folder.Path, err)
	}
	files, err := ParseDiff(folder.Label, text)
	if err != nil {
		return nil, false, fmt.Errorf("parsing the diff in %s: %w", folder.Path, err)
	}

	untracked, err := collectUntracked(ctx, g, folder)
	if err != nil {
		return nil, false, err
	}
	files = append(files, untracked...)

	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, false, nil
}

// collectUntracked diffs each untracked file against /dev/null, which shows it
// as new without writing the operator's index.
func collectUntracked(ctx context.Context, g *gitRunner, folder Folder) ([]File, error) {
	// From a subdirectory ls-files already limits itself to that prefix and
	// prints paths relative to it; --relative is a diff flag and is rejected
	// here.
	out, err := g.run(ctx, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("listing untracked files in %s: %w", folder.Path, err)
	}

	var files []File
	for _, p := range strings.Split(out, "\x00") {
		if p == "" {
			continue
		}
		full := filepath.Join(folder.Path, filepath.FromSlash(p))
		info, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", full, err)
		}

		if info.Size() > UntrackedLimit {
			files = append(files, File{
				Folder: folder.Label,
				Path:   p,
				Header: []string{"diff --git a/" + p + " b/" + p},
				Note:   fmt.Sprintf("untracked, %s, not shown", formatSize(info.Size())),
			})
			continue
		}

		text, err := g.noIndex(ctx, p)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			// An empty untracked file differs from nothing; keep it visible.
			files = append(files, File{
				Folder: folder.Label,
				Path:   p,
				Header: []string{"diff --git a/" + p + " b/" + p},
			})
			continue
		}
		parsed, err := ParseDiff(folder.Label, text)
		if err != nil {
			return nil, fmt.Errorf("parsing the diff of untracked %s: %w", p, err)
		}
		files = append(files, parsed...)
	}
	return files, nil
}

// gitRunner is host git for one folder, with the settings a container could
// have tampered with forced off.
type gitRunner struct {
	dir   string
	flags []string
}

// newGitRunner blanks every configured filter driver. A .gitattributes in the
// checkout names one, and without this host git would run the operator's
// configured program on it; --no-ext-diff and --no-textconv are the same
// defence for diff drivers, which have no equivalent blanket flag.
func newGitRunner(ctx context.Context, dir string) *gitRunner {
	flags := append([]string{}, gitSafeArgs...)
	return &gitRunner{dir: dir, flags: append(flags, blankedFilters(ctx, dir)...)}
}

// blankedFilters reads the driver names from the folder's merged config.
func blankedFilters(ctx context.Context, dir string) []string {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, gitSafeArgs...),
		"config", "--get-regexp", `^filter\.`)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// No drivers configured, or no readable config: nothing to blank.
		return nil
	}
	var args []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		key, _, _ := strings.Cut(line, " ")
		rest, ok := strings.CutPrefix(key, "filter.")
		if !ok {
			continue
		}
		driver, _, ok := strings.Cut(rest, ".")
		if !ok || driver == "" || seen[driver] {
			continue
		}
		seen[driver] = true
		// required=false as well: a required filter with no program fails the
		// whole diff, which would make an agent's .gitattributes a denial of
		// service rather than a no-op.
		args = append(args,
			"-c", "filter."+driver+".clean=",
			"-c", "filter."+driver+".process=",
			"-c", "filter."+driver+".smudge=",
			"-c", "filter."+driver+".required=false")
	}
	return args
}

// run runs host git and returns stdout.
func (g *gitRunner) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, g.flags...), args...)...)
	cmd.Dir = g.dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", gitError(err, stderr.String(), g.dir, args)
	}
	return string(out), nil
}

// noIndex diffs path against /dev/null. Exit status 1 means the files differ,
// which is the success this call exists to observe.
func (g *gitRunner) noIndex(ctx context.Context, path string) (string, error) {
	args := append([]string{"diff", "--no-index"}, diffSafeArgs...)
	args = append(args, "--", os.DevNull, path)
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, g.flags...), args...)...)
	cmd.Dir = g.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return stdout.String(), nil
		}
		return "", gitError(err, stderr.String(), g.dir, args)
	}
	return stdout.String(), nil
}

func gitError(err error, stderr, dir string, args []string) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("git %s in %s: %s", strings.Join(args, " "), dir, msg)
}

// Labels gives each folder the label its files are shown under. A repeated
// basename is ambiguous, so those folders use their full path.
func Labels(paths []string) []Folder {
	if len(paths) == 0 {
		return nil
	}
	if len(paths) == 1 {
		return []Folder{{Path: paths[0]}}
	}

	counts := make(map[string]int, len(paths))
	for _, p := range paths {
		counts[filepath.Base(p)]++
	}

	out := make([]Folder, len(paths))
	for i, p := range paths {
		label := filepath.Base(p)
		if counts[label] > 1 {
			label = p
		}
		out[i] = Folder{Label: label, Path: p}
	}
	return out
}

// formatSize renders a byte count the way the review file's note does.
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
