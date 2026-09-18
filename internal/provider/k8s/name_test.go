package k8s

import (
	"regexp"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// What Kubernetes accepts for a label value, which is the stricter of the two
// places a slug is used.
var labelValue = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)

func container(workspace, name string) model.Container {
	return model.Container{WorkspaceName: workspace, Name: name}
}

func TestSlugIsAValidLabelValue(t *testing.T) {
	names := []string{
		"api",
		"My_Api",
		"UPPER",
		"dots.in.name",
		"under_scores",
		"trailing-",
		"-leading",
		"...",
		"___",
		strings.Repeat("long", 40),
		"a",
	}
	for _, n := range names {
		got := slug(n)
		if !labelValue.MatchString(got) {
			t.Errorf("slug(%q) = %q, which Kubernetes would reject", n, got)
		}
		if len(got) > maxSlug {
			t.Errorf("slug(%q) is %d chars, over the %d budget", n, len(got), maxSlug)
		}
	}
}

// Lowercasing alone would map these onto one object, and the second create
// would silently adopt the first's container.
func TestSlugSeparatesNamesThatDifferOnlyInCase(t *testing.T) {
	pairs := [][2]string{
		{"My_Api", "my-api"},
		{"API", "api"},
		{"a.b", "a_b"},
	}
	for _, p := range pairs {
		if slug(p[0]) == slug(p[1]) {
			t.Errorf("slug(%q) and slug(%q) both give %q", p[0], p[1], slug(p[0]))
		}
	}
}

// Object names are derived, never stored, so an unstable slug would orphan
// every object created by an earlier run.
func TestSlugIsStable(t *testing.T) {
	const name = "My_Api"
	first := slug(name)
	for i := 0; i < 3; i++ {
		if got := slug(name); got != first {
			t.Fatalf("slug(%q) = %q on call %d, was %q", name, got, i+2, first)
		}
	}
}

// Two workspaces holding a container of the same name is the point of
// workspaces, so their objects must not collide in one namespace.
func TestObjectNamesAreWorkspaceScoped(t *testing.T) {
	a := objectName(container("alpha", "api"))
	b := objectName(container("beta", "api"))
	if a == b {
		t.Fatalf("both workspaces produced the object name %q", a)
	}
	for _, n := range []string{a, b} {
		if !strings.HasPrefix(n, "dev-") {
			t.Errorf("object name %q is not namespaced by a dev- prefix", n)
		}
		if len(n) > 253 {
			t.Errorf("object name %q is %d chars, over the DNS-1123 limit", n, len(n))
		}
	}
	if secretName(container("alpha", "api")) != a+"-env" {
		t.Errorf("secret name does not derive from the object name")
	}
}

func TestLabelsAndSelectorAgree(t *testing.T) {
	c := container("My_Workspace", "My_Api")
	l := labels(c)

	for _, part := range strings.Split(selector(c), ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			t.Fatalf("selector part %q is not key=value", part)
		}
		if l[k] != v {
			t.Errorf("selector says %s=%s, labels say %s=%s", k, v, k, l[k])
		}
	}
	if len(l) != 2 {
		t.Errorf("labels = %v, want exactly the workspace and container pair", l)
	}
}

func TestAnnotationsKeepTheTypedNames(t *testing.T) {
	c := container("My_Workspace", "My_Api")
	a := annotations(c)
	if a[annWorkspace] != "My_Workspace" || a[annContainer] != "My_Api" {
		t.Errorf("annotations = %v, want the names as typed", a)
	}
}

func TestImageTag(t *testing.T) {
	c := container("ws", "api")
	want := imageTag("reg.example/dev", c)

	if got := imageTag("reg.example/dev/", c); got != want {
		t.Errorf("a trailing slash on the registry changed the tag: %q vs %q", got, want)
	}
	if !strings.HasSuffix(want, ":latest") {
		t.Errorf("imageTag = %q, want a :latest tag", want)
	}
	if !strings.HasPrefix(want, "reg.example/dev/") {
		t.Errorf("imageTag = %q, want it under the registry prefix", want)
	}
}
