package k8s

import (
	"strings"
	"testing"
)

func TestParseConfigAppliesDefaults(t *testing.T) {
	c, err := ParseConfig(`{"registry":"reg.example/dev"}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if c.Platform != defaultPlatform {
		t.Errorf("Platform = %q, want %q", c.Platform, defaultPlatform)
	}
	if c.Namespace != defaultNamespace {
		t.Errorf("Namespace = %q, want %q", c.Namespace, defaultNamespace)
	}
	if c.StorageSize != defaultStorageSize {
		t.Errorf("StorageSize = %q, want %q", c.StorageSize, defaultStorageSize)
	}
}

func TestParseConfigKeepsExplicitValues(t *testing.T) {
	c, err := ParseConfig(`{
	  "registry":"reg.example/dev",
	  "namespace":"dev-sandboxes",
	  "platform":"linux/arm64",
	  "storageSize":"50Gi",
	  "storageClass":"fast",
	  "serviceAccount":"builder",
	  "imagePullSecret":"regcred",
	  "context":"prod"
	}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	for _, tc := range []struct{ got, want string }{
		{c.Registry, "reg.example/dev"},
		{c.Namespace, "dev-sandboxes"},
		{c.Platform, "linux/arm64"},
		{c.StorageSize, "50Gi"},
		{c.StorageClass, "fast"},
		{c.ServiceAccount, "builder"},
		{c.ImagePullSecret, "regcred"},
		{c.Context, "prod"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}

// Without a registry the build pushes somewhere unintended and the failure
// surfaces a long way from the missing setting.
func TestParseConfigRequiresARegistry(t *testing.T) {
	if _, err := ParseConfig(`{}`); err == nil {
		t.Error("ParseConfig accepted a config with no registry")
	}
	if _, err := ParseConfig(``); err == nil {
		t.Error("ParseConfig accepted an empty config")
	}
}

func TestParseConfigRejectsNonsense(t *testing.T) {
	if _, err := ParseConfig(`{"registry":"reg.example/dev","platform":"amd64"}`); err == nil {
		t.Error("ParseConfig accepted a platform with no os/ prefix")
	}
	if _, err := ParseConfig(`not json`); err == nil {
		t.Error("ParseConfig accepted invalid JSON")
	}
}

// Defaults are written into the stored config rather than left implicit, so a
// later change to a default cannot move a provider that already exists.
func TestMarshalRoundTripsWithDefaults(t *testing.T) {
	raw, err := Config{Registry: "reg.example/dev"}.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(raw, defaultPlatform) {
		t.Errorf("stored config %q does not record the platform default", raw)
	}

	back, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if back.Platform != defaultPlatform || back.Registry != "reg.example/dev" {
		t.Errorf("round trip lost data: %+v", back)
	}
}
