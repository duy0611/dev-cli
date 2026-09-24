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
		script := `set -e; t=$(mktemp -d); trap 'rm -rf "$t"' EXIT; ` +
			`git clone -q --depth 1` + branch + ` -- "$1" "$t/r"; ` +
			`[ -f "$t/r/$2/SKILL.md" ] || { echo "no SKILL.md at $2 in $1" >&2; exit 1; }; ` +
			`d=` + target + `; rm -rf "$d"; mkdir -p "$(dirname "$d")"; ` +
			`cp -R "$t/r/$2" "$d"; rm -rf "$d/.git"`
		out = append(out, Step{
			Desc: desc,
			Cmd:  []string{"sh", "-c", script, "sh", sk.Git, sk.Path, sk.Ref},
		})
	}
	return out
}
