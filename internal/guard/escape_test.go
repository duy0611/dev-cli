package guard

import (
	"strings"
	"testing"
)

const home = "/Users/me"

func check(t *testing.T, merged string) []Finding {
	t.Helper()
	got, err := Escape([]byte(merged), home)
	if err != nil {
		t.Fatalf("Escape: %v", err)
	}
	return got
}

func want(t *testing.T, merged, substr string) {
	t.Helper()
	got := check(t, merged)
	for _, f := range got {
		if strings.Contains(f.What, substr) {
			return
		}
	}
	t.Errorf("no finding mentioning %q in %v\nfrom %s", substr, got, merged)
}

func clean(t *testing.T, merged string) {
	t.Helper()
	if got := check(t, merged); len(got) != 0 {
		t.Errorf("findings %v, want none\nfrom %s", got, merged)
	}
}

// Every way a configuration can ask for the host, each from the place it
// actually arrives: the merged document (where a feature's privileged has
// already been OR'd in), a mount in either spelling, and runArgs.
func TestEscapeFindsEachRoute(t *testing.T) {
	cases := map[string]struct{ merged, substr string }{
		"privileged":            {`{"privileged": true}`, "privileged"},
		"runArgs privileged":    {`{"runArgs": ["--privileged"]}`, "privileged"},
		"runArgs privileged=":   {`{"runArgs": ["--privileged=true"]}`, "privileged"},
		"docker socket string":  {`{"mounts": ["source=/var/run/docker.sock,target=/var/run/docker.sock,type=bind"]}`, "docker.sock"},
		"docker socket object":  {`{"mounts": [{"source": "/var/run/docker.sock", "target": "/sock", "type": "bind"}]}`, "docker.sock"},
		"podman socket":         {`{"mounts": ["source=/run/podman/podman.sock,target=/p,type=bind"]}`, "podman.sock"},
		"user docker socket":    {`{"mounts": ["src=/Users/me/.docker/run/docker.sock,dst=/d,type=bind"]}`, "docker.sock"},
		"runArgs volume socket": {`{"runArgs": ["-v", "/var/run/docker.sock:/var/run/docker.sock"]}`, "docker.sock"},
		"runArgs mount socket":  {`{"runArgs": ["--mount", "type=bind,source=/var/run/docker.sock,target=/s"]}`, "docker.sock"},
		"host root":             {`{"mounts": ["source=/,target=/host,type=bind"]}`, "host filesystem"},
		"home":                  {`{"mounts": ["source=/Users/me,target=/h,type=bind"]}`, "host filesystem"},
		"home with slash":       {`{"mounts": ["source=/Users/me/,target=/h,type=bind"]}`, "host filesystem"},
		"parent of home":        {`{"mounts": ["source=/Users,target=/u,type=bind"]}`, "host filesystem"},
		"runArgs volume home":   {`{"runArgs": ["--volume=/Users/me:/h"]}`, "host filesystem"},
		"pid host":              {`{"runArgs": ["--pid=host"]}`, "--pid=host"},
		"pid host split":        {`{"runArgs": ["--pid", "host"]}`, "--pid=host"},
		"network host":          {`{"runArgs": ["--network=host"]}`, "--network=host"},
		"net host":              {`{"runArgs": ["--net", "host"]}`, "--network=host"},
		"ipc host":              {`{"runArgs": ["--ipc=host"]}`, "--ipc=host"},
		"userns host":           {`{"runArgs": ["--userns=host"]}`, "--userns=host"},
		"uts host":              {`{"runArgs": ["--uts=host"]}`, "--uts=host"},
		"device":                {`{"runArgs": ["--device=/dev/kvm"]}`, "--device"},
		"device split":          {`{"runArgs": ["--device", "/dev/fuse"]}`, "--device"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { want(t, c.merged, c.substr) })
	}
}

// These widen what root can do inside the container, not the way out of it —
// and the Go feature in dev's own catalog asks for the first two.
func TestEscapeAllowsWhatStaysInside(t *testing.T) {
	for name, merged := range map[string]string{
		"empty":                           `{}`,
		"go feature":                      `{"capAdd": ["SYS_PTRACE"], "securityOpt": ["seccomp=unconfined"], "init": true}`,
		"not privileged":                  `{"privileged": false}`,
		"volume mount":                    `{"mounts": ["source=dev-ws-api-state,target=/var/dev-state,type=volume"]}`,
		"project bind":                    `{"mounts": ["source=/Users/me/src/api,target=/workspaces/api,type=bind"]}`,
		"sibling of home":                 `{"mounts": ["source=/Users/mel,target=/x,type=bind"]}`,
		"benign runArgs":                  `{"runArgs": ["--cap-add=NET_ADMIN", "--shm-size=1g", "--network=devnet", "--add-host=a:1.2.3.4"]}`,
		"tmpfs":                           `{"mounts": ["type=tmpfs,target=/tmp"]}`,
		"named volume called docker.sock": `{"mounts": ["source=docker.sock,target=/x,type=volume"]}`,
	} {
		t.Run(name, func(t *testing.T) { clean(t, merged) })
	}
}

// A finding says where it came from, so the operator knows which file — or
// which feature — to look at.
func TestEscapeFindingsSayWhere(t *testing.T) {
	got := check(t, `{"privileged": true, "runArgs": ["--pid=host"], "mounts": ["source=/,target=/h,type=bind"]}`)
	if len(got) != 3 {
		t.Fatalf("got %d findings, want 3: %v", len(got), got)
	}
	from := map[string]bool{}
	for _, f := range got {
		from[f.From] = true
	}
	for _, w := range []string{"privileged", "runArgs", "mounts"} {
		if !from[w] {
			t.Errorf("no finding from %q: %v", w, got)
		}
	}
}

// The same route asked for twice is reported once.
func TestEscapeDeduplicates(t *testing.T) {
	got := check(t, `{"privileged": true, "runArgs": ["--privileged"]}`)
	n := 0
	for _, f := range got {
		if strings.Contains(f.What, "privileged") {
			n++
		}
	}
	if n != 2 {
		// One per source is the useful answer: both places need changing.
		t.Errorf("got %d privileged findings, want one per source (2): %v", n, got)
	}
	got = check(t, `{"runArgs": ["--pid=host", "--pid", "host"]}`)
	if len(got) != 1 {
		t.Errorf("repeated runArgs: got %v, want one finding", got)
	}
}

func TestEscapeRejectsMalformedInput(t *testing.T) {
	if _, err := Escape([]byte(`not json`), home); err == nil {
		t.Error("accepted malformed JSON")
	}
	// A mounts field that is not a list is malformed, not safe.
	if _, err := Escape([]byte(`{"mounts": "source=/,target=/h"}`), home); err == nil {
		t.Error("accepted a non-list mounts")
	}
}

func TestComposeFindsEachRoute(t *testing.T) {
	file := `
services:
  app:
    image: x
    privileged: true
    volumes:
      - ./src:/workspace
      - /var/run/docker.sock:/var/run/docker.sock
      - type: bind
        source: /Users/me
        target: /h
      - data:/data
  sidecar:
    image: y
    network_mode: host
    pid: host
    devices:
      - /dev/kvm:/dev/kvm
volumes:
  data: {}
`
	got, err := Compose("docker-compose.yml", []byte(file), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, substr := range []string{"privileged", "docker.sock", "host filesystem", "--network=host", "--pid=host", "--device /dev/kvm"} {
		found := false
		for _, f := range got {
			if strings.Contains(f.What, substr) {
				found = true
			}
		}
		if !found {
			t.Errorf("no finding for %q in %v", substr, got)
		}
	}
	for _, f := range got {
		if strings.Contains(f.What, "/workspace") || strings.Contains(f.What, "data") {
			t.Errorf("a project-relative or named volume was flagged: %v", f)
		}
		if !strings.Contains(f.From, "docker-compose.yml service ") {
			t.Errorf("finding does not name its file and service: %v", f)
		}
	}
}

func TestComposeCleanFileHasNoFindings(t *testing.T) {
	got, err := Compose("c.yml", []byte("services:\n  app:\n    image: x\n    cap_add: [SYS_PTRACE]\n    volumes: [./src:/w]\n"), home)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want none", got, err)
	}
}

// A compose file the check cannot read has not passed.
func TestComposeUnparseableIsAnError(t *testing.T) {
	if _, err := Compose("c.yml", []byte("services: [unclosed"), home); err == nil {
		t.Error("accepted a compose file that does not parse")
	}
}
