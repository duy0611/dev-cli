package agent

import (
	"strings"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// skillSteps places each skill at <dir>/<name>, replacing only that directory,
// so skills added by hand survive. Git skills from one repository at one ref
// share a step and a clone.
//
// dir and sk.Name are spliced into the script; both are constants or pass
// validSkillName. Free text from agents.yaml is passed as arguments instead.
func skillSteps(agentID, dir string, skills []agentcfg.Skill) []Step {
	type repo struct{ git, ref string }
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
				// -o: files belong to the extracting user, not the archive's
				// owner.
				Cmd:   []string{"sh", "-c", `d=` + dir + "/" + sk.Name + `; rm -rf "$d" && mkdir -p "$d" && tar -xo -C "$d"`},
				Stdin: sk.Archive,
			})
			continue
		}
		out = append(out, gitSkillStep(agentID, dir, o.repo.git, o.repo.ref, groups[o.repo]))
	}
	return out
}

// gitSkillStep clones one repository and installs each skill asked for.
// Arguments are the URL, the ref, then a name and a subdirectory per skill.
//
// Staging is in a hidden directory beside the target, not $TMPDIR: under
// SELinux a file keeps its label across mv, and /tmp carries the container's
// own MCS categories, so a skill staged there becomes unreadable after a
// rebuild.
//
// Each skill path is resolved with realpath and must land inside the clone, and
// cp -RP copies links as links. .git is removed before the move, so nothing is
// deleted through a symlink.
//
// All skills are staged before any is installed, so one bad subdirectory
// replaces none of them. The staged names go through $ns unquoted;
// validSkillName forbids whitespace and glob characters, which keeps that safe.
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
