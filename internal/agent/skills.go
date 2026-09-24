package agent

import (
	"strings"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// skillSteps places each skill at <dir>/<name>, replacing whatever that one
// directory held. Directories the file does not name are left alone, so a
// skill the operator added by hand survives an apply.
//
// Git skills from one repository at one ref share a step and a clone, in the
// position of the first of them: a repository holding ten skills is cloned
// once, not ten times, however the file spreads them across entries.
//
// dir is an adapter's constant shell expression and sk.Name passed
// validSkillName — letters, digits, . _ - and never "." or ".." — so both are
// safe to splice. Everything that came from agents.yaml as free text (a URL, a
// ref, a subdirectory) is passed as a positional argument instead.
func skillSteps(agentID, dir string, skills []agentcfg.Skill) []Step {
	type repo struct{ git, ref string }
	// A slot is one step: a local skill, or the repository a group is keyed by.
	type slot struct {
		local *agentcfg.Skill
		repo  repo
	}
	var order []slot
	groups := map[repo][]agentcfg.Skill{}
	for i, sk := range skills {
		if sk.Archive != nil {
			order = append(order, slot{local: &skills[i]})
			continue
		}
		k := repo{sk.Git, sk.Ref}
		if _, ok := groups[k]; !ok {
			order = append(order, slot{repo: k})
		}
		groups[k] = append(groups[k], sk)
	}

	var out []Step
	for _, o := range order {
		if sk := o.local; sk != nil {
			out = append(out, Step{
				Desc: agentID + ": skill " + sk.Name,
				// -o: do not restore ownership from the archive, so the
				// files belong to the user running the extraction.
				Cmd:   []string{"sh", "-c", `d=` + dir + "/" + sk.Name + `; rm -rf "$d" && mkdir -p "$d" && tar -xo -C "$d"`},
				Stdin: sk.Archive,
			})
			continue
		}
		out = append(out, gitSkillStep(agentID, dir, o.repo.git, o.repo.ref, groups[o.repo]))
	}
	return out
}

// gitSkillStep clones one repository and installs every skill it was asked
// for. Arguments are the URL, the ref, then a name and a subdirectory per skill.
//
// Cloned per agent into a temporary directory the trap removes. A shallow
// clone is cheap, and sharing one across agents would need state carried
// between steps that are otherwise independent.
//
// That directory is a hidden one inside the skills directory, never $TMPDIR.
// Under SELinux a file keeps its label across a rename, and /tmp is labelled
// with the container's own MCS categories while the state volume is not: a
// skill staged in /tmp and moved into place is readable by that container
// alone, so once a rebuild gives the container new categories the next apply
// cannot even remove it. Staged beside its target, every file is created on
// the volume and labelled as the volume is, and the final mv stays a rename
// within one filesystem.
//
// The repository is third-party content, so each skill directory is resolved
// with realpath and has to land inside the clone: a path that is, or passes
// through, a symlink out of it would otherwise install a link to anywhere, and
// the .git removal below would delete through it. cp -RP copies any links
// inside the skill as links, never following them, and .git is removed from
// the copy before it moves into place, so nothing is ever deleted through the
// target.
//
// Every skill is checked and staged before any is installed, so one bad
// subdirectory fails the step with none of the repository's skills replaced,
// rather than leaving half of them at the new version and half at the old.
// The staged names go through $ns unquoted, which is safe only because
// validSkillName allows no whitespace or glob characters.
func gitSkillStep(agentID, dir, git, ref string, skills []agentcfg.Skill) Step {
	branch := ""
	if ref != "" {
		branch = ` --branch "$2"`
	}
	script := `set -e; d=` + dir + `; mkdir -p "$d"; ` +
		`t=$(mktemp -d "$d/.dev-stage.XXXXXX"); trap 'rm -rf "$t"' EXIT; ` +
		`git clone -q --depth 1` + branch + ` -- "$1" "$t/r"; ` +
		`u=$1; shift 2; r=$(realpath "$t/r"); mkdir "$t/s"; ns=; ` +
		`while [ $# -gt 0 ]; do n=$1; p=$2; shift 2; ` +
		`s=$(realpath "$t/r/$p" 2>/dev/null) || s=; ` +
		`case "$s/" in "$r/"*) ;; *) echo "$p is outside the repository $u" >&2; exit 1;; esac; ` +
		`[ ! -L "$t/r/$p" ] && [ -d "$s" ] && [ -f "$s/SKILL.md" ] && [ ! -L "$s/SKILL.md" ] || ` +
		`{ echo "no SKILL.md at $p in $u" >&2; exit 1; }; ` +
		`cp -RP "$s" "$t/s/$n"; rm -rf "$t/s/$n/.git"; ns="$ns $n"; done; ` +
		`for n in $ns; do rm -rf "$d/$n"; mv "$t/s/$n" "$d/$n"; done`

	args := []string{"sh", "-c", script, "sh", git, ref}
	names := make([]string, 0, len(skills))
	for _, sk := range skills {
		args = append(args, sk.Name, sk.Path)
		names = append(names, sk.Name)
	}
	desc := agentID + ": skill " + names[0]
	if len(names) > 1 {
		desc = agentID + ": skills " + strings.Join(names, ", ")
	}
	return Step{Desc: desc, Cmd: args}
}
