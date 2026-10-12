package guard

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Compose returns the host routes in a compose file, for every service. The
// configuration names one service, but the others start too. A file that does
// not parse is an error, not an empty result.
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

// composeVolume reads one volume entry in either compose spelling. A relative
// source is resolved against the project, so it cannot name the host.
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
