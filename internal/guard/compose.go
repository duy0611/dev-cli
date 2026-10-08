package guard

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Compose returns everything in one compose file that gives any of its
// services a way to the host — the same routes Escape looks for, in compose's
// own spelling.
//
// A compose project gets no reliable --override-config (invariant 10), and its
// privileges live in the compose file, not the devcontainer.json, so they have
// to be read here. Every service is read rather than only the one the
// configuration names: the others start too, and a privileged sidecar is as
// much a way out as a privileged dev container.
//
// A file that does not parse is an error, never an empty answer — a check that
// cannot read what it is checking has not passed.
func Compose(file string, content []byte, home string) ([]Finding, error) {
	var doc struct {
		Services map[string]struct {
			Privileged  bool     `yaml:"privileged"`
			NetworkMode string   `yaml:"network_mode"`
			Pid         string   `yaml:"pid"`
			Ipc         string   `yaml:"ipc"`
			Userns      string   `yaml:"userns_mode"`
			Uts         string   `yaml:"uts"`
			Devices     []any    `yaml:"devices"`
			Volumes     []any    `yaml:"volumes"`
			CapAdd      []string `yaml:"cap_add"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}

	var out findings
	for name, s := range doc.Services {
		from := file + " service " + name
		if s.Privileged {
			out.add("privileged: true", from)
		}
		for field, v := range map[string]string{
			"--network=host": s.NetworkMode, "--pid=host": s.Pid, "--ipc=host": s.Ipc,
			"--userns=host": s.Userns, "--uts=host": s.Uts,
		} {
			if v == "host" {
				out.add(field, from)
			}
		}
		for _, d := range s.Devices {
			out.add(fmt.Sprintf("--device %v", deviceSource(d)), from)
		}
		for _, v := range s.Volumes {
			if what := composeVolume(v, home); what != "" {
				out.add(what, from)
			}
		}
	}
	return out.list, nil
}

// composeVolume reads one entry of a service's volumes, in either of compose's
// spellings: "host:container[:opts]" or {type, source, target}.
//
// A relative source ("./data") is resolved by compose against the project
// directory, which is the project, not the host's root or home; only an
// absolute source can name those.
func composeVolume(v any, home string) string {
	switch e := v.(type) {
	case string:
		if src, _, _ := strings.Cut(e, ":"); strings.HasPrefix(src, "/") {
			return hostPath(src, home)
		}
	case map[string]any:
		kind, _ := e["type"].(string)
		src, _ := e["source"].(string)
		return hostMount(mount{kind: kind, source: src}, home)
	}
	return ""
}

func deviceSource(d any) any {
	if s, ok := d.(string); ok {
		src, _, _ := strings.Cut(s, ":")
		return src
	}
	return d
}
