package agent

import "github.com/duy0611/dev-cli/internal/agentcfg"

// skillSteps places each skill at <dir>/<name>, replacing whatever that one
// directory held. Directories the file does not name are left alone, so a
// skill the operator added by hand survives an apply.
//
// dir is an adapter's constant shell expression and sk.Name passed
// validSkillName — letters, digits, . _ - and never "." or ".." — so both are
// safe to splice. Everything that came from agents.yaml as free text (a URL, a
// ref, a subdirectory) is passed as a positional argument instead.
func skillSteps(agentID, dir string, skills []agentcfg.Skill) []Step {
	var out []Step
	for _, sk := range skills {
		target := dir + "/" + sk.Name
		desc := agentID + ": skill " + sk.Name
		if sk.Archive != nil {
			out = append(out, Step{
				Desc: desc,
				// -o: do not restore ownership from the archive, so the
				// files belong to the user running the extraction.
				Cmd:   []string{"sh", "-c", `d=` + target + `; rm -rf "$d" && mkdir -p "$d" && tar -xo -C "$d"`},
				Stdin: sk.Archive,
			})
			continue
		}
		branch := ""
		if sk.Ref != "" {
			branch = ` --branch "$3"`
		}
		// Cloned per agent into a temporary directory the trap removes. A
		// shallow clone is cheap, and sharing one across agents would need
		// state carried between steps that are otherwise independent.
		//
		// The repository is third-party content, so the skill directory is
		// resolved with realpath and has to land inside the clone: a path
		// that is, or passes through, a symlink out of it would otherwise
		// install a link to anywhere, and the .git removal below would delete
		// through it. cp -RP copies any links inside the skill as links, never
		// following them, and .git is removed from the copy before it moves
		// into place, so nothing is ever deleted through the target.
		script := `set -e; t=$(mktemp -d); trap 'rm -rf "$t"' EXIT; ` +
			`git clone -q --depth 1` + branch + ` -- "$1" "$t/r"; ` +
			`r=$(realpath "$t/r"); s=$(realpath "$t/r/$2" 2>/dev/null) || s=; ` +
			`case "$s/" in "$r/"*) ;; *) echo "$2 is outside the repository $1" >&2; exit 1;; esac; ` +
			`[ ! -L "$t/r/$2" ] && [ -d "$s" ] && [ -f "$s/SKILL.md" ] && [ ! -L "$s/SKILL.md" ] || ` +
			`{ echo "no SKILL.md at $2 in $1" >&2; exit 1; }; ` +
			`cp -RP "$s" "$t/skill"; rm -rf "$t/skill/.git"; ` +
			`d=` + target + `; rm -rf "$d"; mkdir -p "$(dirname "$d")"; mv "$t/skill" "$d"`
		out = append(out, Step{
			Desc: desc,
			Cmd:  []string{"sh", "-c", script, "sh", sk.Git, sk.Path, sk.Ref},
		})
	}
	return out
}
