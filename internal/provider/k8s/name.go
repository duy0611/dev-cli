package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
)

// Label keys, the same pair the local provider sets on a container.
const (
	labelWorkspace = "dev.workspace"
	labelContainer = "dev.container"
)

// Annotation keys holding the names as typed, before slugging.
const (
	annWorkspace = "dev.workspace-name"
	annContainer = "dev.container-name"
)

// Slug length budget. Label values are capped at 63 characters, so the slug is
// sized for that and shared with object names.
const (
	maxSlug     = 50
	hashLen     = 6
	maxSlugBody = maxSlug - hashLen - 1 // the '-' before the hash
)

// slug converts a name into a DNS-1123 label. Lowercasing alone would map
// `My_Api` and `my-api` to one object, so the hash of the original is what
// keeps them apart.
func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	body := strings.Trim(b.String(), "-")
	if len(body) > maxSlugBody {
		body = strings.Trim(body[:maxSlugBody], "-")
	}
	// Punctuation-only names leave an empty body; a label value may not start
	// with '-'.
	if body == "" {
		body = "x"
	}

	sum := sha256.Sum256([]byte(name))
	return body + "-" + hex.EncodeToString(sum[:])[:hashLen]
}

// objectName names a container's Deployment and PVC. Both slugs are included so
// same-named containers in different workspaces do not collide.
func objectName(c model.Container) string {
	return fmt.Sprintf("dev-%s-%s", slug(c.WorkspaceName), slug(c.Name))
}

// secretName is the env Secret for a container.
func secretName(c model.Container) string {
	return objectName(c) + "-env"
}

// labels identify a container's objects, and are what every lookup selects on.
func labels(c model.Container) map[string]string {
	return map[string]string{
		labelWorkspace: slug(c.WorkspaceName),
		labelContainer: slug(c.Name),
	}
}

// selector renders labels as a kubectl -l argument.
func selector(c model.Container) string {
	return fmt.Sprintf("%s=%s,%s=%s",
		labelWorkspace, slug(c.WorkspaceName),
		labelContainer, slug(c.Name))
}

// annotations hold the names as typed, for display; labels hold the slugs.
func annotations(c model.Container) map[string]string {
	return map[string]string{
		annWorkspace: c.WorkspaceName,
		annContainer: c.Name,
	}
}

// imageTag is where this container's image is pushed. The tag is always
// :latest, so the Deployment must pull Always.
func imageTag(registry string, c model.Container) string {
	return fmt.Sprintf("%s/%s-%s:latest",
		strings.TrimSuffix(registry, "/"), slug(c.WorkspaceName), slug(c.Name))
}
