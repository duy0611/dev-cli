// Package agentcfg reads the agents.yaml a project declares its agents' skills,
// MCP servers and plugins in.
//
// It parses and validates and nothing else: no exec, no cobra, no container.
// Turning a declaration into commands is internal/agent's job. The only
// filesystem access is reading the file and the local skills it names, which
// happens here so that a missing skill is a usage error at create rather than a
// failure halfway through an apply.
package agentcfg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/duy0611/dev-cli/internal/xpath"
	"go.yaml.in/yaml/v3"
)

// FileName is where a project declares its agents, relative to its folder.
const FileName = ".devcontainer/agents.yaml"

// agents are the ids this file has a section for. Codex is in the agent
// registry but deliberately not here, so a codex: section is an unknown key
// rather than a declaration nothing applies.
var agents = []string{"claude", "hermes", "opencode"}

// Skill is one SKILL.md directory to place in an agent's skills directory.
type Skill struct {
	// Name is the directory it lands in, validated as a plain name.
	Name string
	// Git, Ref and Path describe a skill cloned inside the container. Path is
	// the subdirectory holding SKILL.md, "." for the repository root.
	Git  string
	Ref  string
	Path string
	// Archive is a local skill as a tar, read on the host at load. Non-nil
	// exactly when the skill is local.
	Archive []byte
}

// MCPServer is one MCP server: local (Command) or remote (URL), never both.
// Values may carry ${NAME} references; see RewriteRefs.
type MCPServer struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

// Remote reports whether this is a server reached over the network.
func (m MCPServer) Remote() bool { return m.URL != "" }

// View is what one agent receives: the shared layer plus its own section.
type View struct {
	Skills       []Skill
	MCP          map[string]MCPServer
	Plugins      []string
	Marketplaces map[string]string
}

// Spec is a loaded, validated agents.yaml.
type Spec struct {
	shared   section
	sections map[string]*section
}

type section struct {
	skills       []Skill
	mcp          map[string]MCPServer
	plugins      []string
	marketplaces map[string]string
}

// The raw shapes, decoded with KnownFields so that a key an agent does not have
// — hermes.plugins, opencode.marketplaces, codex — is rejected rather than
// ignored. A typo caught at create beats a plugin that silently never arrives.
type rawSkill struct {
	Git  string `yaml:"git"`
	Ref  string `yaml:"ref"`
	Path string `yaml:"path"`
	// Paths names several skills in one repository. Nil when absent, which
	// is how an empty list is told apart from none given.
	Paths []string `yaml:"paths"`
}

type rawClaude struct {
	Marketplaces map[string]string    `yaml:"marketplaces"`
	Plugins      []string             `yaml:"plugins"`
	Skills       []rawSkill           `yaml:"skills"`
	MCP          map[string]MCPServer `yaml:"mcp"`
}

type rawOpencode struct {
	Plugins []string             `yaml:"plugins"`
	Skills  []rawSkill           `yaml:"skills"`
	MCP     map[string]MCPServer `yaml:"mcp"`
}

type rawHermes struct {
	Skills []rawSkill           `yaml:"skills"`
	MCP    map[string]MCPServer `yaml:"mcp"`
}

type rawFile struct {
	// A pointer so that a missing version is told apart from version: 0.
	Version  *int                 `yaml:"version"`
	Skills   []rawSkill           `yaml:"skills"`
	MCP      map[string]MCPServer `yaml:"mcp"`
	Claude   *rawClaude           `yaml:"claude"`
	Opencode *rawOpencode         `yaml:"opencode"`
	Hermes   *rawHermes           `yaml:"hermes"`
}

// Load reads and validates an agents.yaml. Every error names the file.
func Load(file string) (*Spec, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var raw rawFile
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty; it needs at least \"version: 1\"", file)
		}
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if raw.Version == nil {
		return nil, fmt.Errorf("%s: version is required (version: 1)", file)
	}
	if *raw.Version != 1 {
		return nil, fmt.Errorf("%s: version %d is not one this dev reads (version: 1)", file, *raw.Version)
	}

	// Local skill paths are relative to the file, not to the working
	// directory, so --agent-config ~/dotfiles/agents.yaml carries skills that
	// sit next to it.
	base := filepath.Dir(file)
	s := &Spec{sections: map[string]*section{}}
	if s.shared, err = buildSection(file, base, "", raw.Skills, raw.MCP); err != nil {
		return nil, err
	}
	if c := raw.Claude; c != nil {
		sec, err := buildSection(file, base, "claude.", c.Skills, c.MCP)
		if err != nil {
			return nil, err
		}
		for name := range c.Marketplaces {
			if err := xpath.ValidateName(name); err != nil {
				return nil, fmt.Errorf("%s: claude.marketplaces: %w", file, err)
			}
		}
		sec.plugins, sec.marketplaces = c.Plugins, c.Marketplaces
		s.sections["claude"] = &sec
	}
	if o := raw.Opencode; o != nil {
		sec, err := buildSection(file, base, "opencode.", o.Skills, o.MCP)
		if err != nil {
			return nil, err
		}
		sec.plugins = o.Plugins
		s.sections["opencode"] = &sec
	}
	if h := raw.Hermes; h != nil {
		sec, err := buildSection(file, base, "hermes.", h.Skills, h.MCP)
		if err != nil {
			return nil, err
		}
		s.sections["hermes"] = &sec
	}

	// Checked per agent, across both layers: two skills with one name would
	// land in one directory, and whichever ran second would silently win.
	for _, id := range agents {
		v, ok := s.View(id)
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, sk := range v.Skills {
			if seen[sk.Name] {
				return nil, fmt.Errorf("%s: duplicate skill %q for %s", file, sk.Name, id)
			}
			seen[sk.Name] = true
		}
	}
	return s, nil
}

func buildSection(file, base, prefix string, skills []rawSkill, mcp map[string]MCPServer) (section, error) {
	var sec section
	for i, r := range skills {
		sk, err := buildSkills(base, r)
		if err != nil {
			return section{}, fmt.Errorf("%s: %sskills[%d]: %w", file, prefix, i, err)
		}
		sec.skills = append(sec.skills, sk...)
	}
	for name, m := range mcp {
		if err := validateMCP(name, m); err != nil {
			return section{}, fmt.Errorf("%s: %smcp.%s: %w", file, prefix, name, err)
		}
	}
	sec.mcp = mcp
	return sec, nil
}

// buildSkills expands one entry into the skills it names: several for a git
// entry with paths, one otherwise. Each is a Skill of its own, so the
// duplicate-name check in Load sees every name however the entries group them.
func buildSkills(base string, r rawSkill) ([]Skill, error) {
	if r.Paths == nil {
		sk, err := buildSkill(base, r)
		if err != nil {
			return nil, err
		}
		return []Skill{sk}, nil
	}
	switch {
	case r.Git == "":
		// A local entry already names one directory; a list of them is a
		// list of entries.
		return nil, errors.New("paths only applies to a git skill; give each local skill its own entry")
	case r.Path != "":
		return nil, errors.New("give path or paths, not both")
	case len(r.Paths) == 0:
		return nil, errors.New("paths is empty; give at least one subdirectory")
	}
	out := make([]Skill, 0, len(r.Paths))
	for _, p := range r.Paths {
		sk, err := gitSkill(r.Git, r.Ref, p)
		if err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, nil
}

func buildSkill(base string, r rawSkill) (Skill, error) {
	switch {
	case r.Git != "":
		return gitSkill(r.Git, r.Ref, r.Path)

	case r.Path != "":
		if r.Ref != "" {
			return Skill{}, errors.New("ref only applies to a git skill")
		}
		p := r.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		// Named from the path as written, not as resolved: a symlinked
		// skills/foo is still "foo" to the operator who wrote it.
		name := filepath.Base(filepath.Clean(p))
		if err := validSkillName(name); err != nil {
			return Skill{}, err
		}
		dir, err := xpath.Resolve(p)
		if err != nil {
			return Skill{}, err
		}
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			return Skill{}, fmt.Errorf("%s has no SKILL.md", r.Path)
		}
		archive, err := tarDir(dir)
		if err != nil {
			return Skill{}, err
		}
		return Skill{Name: name, Archive: archive}, nil

	default:
		return Skill{}, errors.New("give git or path")
	}
}

// gitSkill is one skill at subdirectory p of a repository, the root when p
// is empty.
func gitSkill(git, ref, p string) (Skill, error) {
	written := p
	if p == "" {
		p = "."
	}
	// IsLocal refuses an absolute path and anything climbing out with "..":
	// the path is spliced into a copy inside the container, and a skill must
	// not be able to name a directory outside its clone.
	if !filepath.IsLocal(p) {
		return Skill{}, fmt.Errorf("path %q must stay inside the repository", written)
	}
	p = filepath.ToSlash(filepath.Clean(p))
	name := path.Base(p)
	if p == "." {
		name = strings.TrimSuffix(path.Base(git), ".git")
	}
	if err := validSkillName(name); err != nil {
		return Skill{}, err
	}
	return Skill{Name: name, Git: git, Ref: ref, Path: p}, nil
}

// validSkillName is xpath.ValidateName minus the two names it accepts that
// would be catastrophic here: the name becomes a directory dev deletes and
// replaces, and "." or ".." there is the skills directory or its parent.
func validSkillName(name string) error {
	if name == "." || name == ".." {
		return fmt.Errorf("skill name %q is not a directory name; give a path ending in one", name)
	}
	return xpath.ValidateName(name)
}

func validateMCP(name string, m MCPServer) error {
	if err := xpath.ValidateName(name); err != nil {
		return err
	}
	switch {
	case m.Command != "" && m.URL != "":
		return errors.New("give command or url, not both")
	case m.Command == "" && m.URL == "":
		return errors.New("give command (a local server) or url (a remote one)")
	case m.Command != "" && len(m.Headers) > 0:
		return errors.New("headers only apply to a remote server")
	case m.URL != "" && (len(m.Args) > 0 || len(m.Env) > 0):
		return errors.New("args and env only apply to a local server")
	}
	return nil
}

// View projects the file onto one agent. False when nothing in it reaches that
// agent, which is what spares a container a probe for an agent the file never
// mentions.
func (s *Spec) View(agent string) (View, bool) {
	if !slices.Contains(agents, agent) {
		return View{}, false
	}
	sec := s.sections[agent]
	if sec == nil && len(s.shared.skills) == 0 && len(s.shared.mcp) == 0 {
		return View{}, false
	}
	v := View{MCP: map[string]MCPServer{}}
	v.Skills = append(v.Skills, s.shared.skills...)
	maps.Copy(v.MCP, s.shared.mcp)
	if sec != nil {
		v.Skills = append(v.Skills, sec.skills...)
		// After the shared layer: a per-agent server of the same name
		// replaces it for this agent, the one override the format allows.
		maps.Copy(v.MCP, sec.mcp)
		v.Plugins = sec.plugins
		v.Marketplaces = sec.marketplaces
	}
	return v, true
}

// refPattern is a ${NAME} reference: an environment variable name in braces
// and nothing else. $NAME, ${1} and ${A:-b} are deliberately not matched —
// each agent resolves only the plain form, so rewriting anything more would
// promise a default no agent implements.
var refPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Refs lists the variables the file's MCP servers reference, for warning about
// ones no workspace setting defines.
func (s *Spec) Refs() []string {
	seen := map[string]bool{}
	collect := func(v string) {
		for _, m := range refPattern.FindAllStringSubmatch(v, -1) {
			seen[m[1]] = true
		}
	}
	all := []section{s.shared}
	for _, sec := range s.sections {
		all = append(all, *sec)
	}
	for _, sec := range all {
		for _, m := range sec.mcp {
			collect(m.URL)
			for _, a := range m.Args {
				collect(a)
			}
			for _, v := range m.Env {
				collect(v)
			}
			for _, v := range m.Headers {
				collect(v)
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// RewriteRefs replaces every ${NAME} in s with to(NAME). Adapters use it to
// spell a reference the way their agent resolves one; dev never substitutes a
// value, which is what keeps tokens out of the file, the database and the
// state volume.
func RewriteRefs(s string, to func(name string) string) string {
	return refPattern.ReplaceAllStringFunc(s, func(m string) string {
		return to(refPattern.FindStringSubmatch(m)[1])
	})
}
