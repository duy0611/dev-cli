package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
)

// Label keys, the same pair the local provider puts on a container. Two
// providers, one vocabulary: every lookup filters on our own keys, and
// `kubectl get -l dev.container=api` reads like `docker ps --filter`.
const (
	labelWorkspace = "dev.workspace"
	labelContainer = "dev.container"
)

// Annotations carrying the names as the operator typed them, since the slug
// below may have changed them.
const (
	annWorkspace = "dev.workspace-name"
	annContainer = "dev.container-name"
)

// Slug length budget. Object names are DNS-1123 (253 chars) but label *values*
// are capped at 63, and the same slug is used for both so the two cannot drift.
// The `dev-` prefix and the `-env` suffix on the Secret come out of that
// budget too.
const (
	maxSlug     = 50
	hashLen     = 6
	maxSlugBody = maxSlug - hashLen - 1 // the '-' before the hash
)

// slug converts a name this tool accepts into one Kubernetes accepts.
//
// `xpath.ValidateName` allows uppercase, dots and underscores; DNS-1123 allows
// none of them. Lowercasing alone would map `My_Api` and `my-api` onto the same
// object, so a short hash of the original is appended — the slug is a display
// aid, the hash is what makes it unique.
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
	// A name of nothing but punctuation leaves an empty body, and a label value
	// may not start with '-'.
	if body == "" {
		body = "x"
	}

	sum := sha256.Sum256([]byte(name))
	return body + "-" + hex.EncodeToString(sum[:])[:hashLen]
}

// objectName is the name shared by a container's Deployment and PVC.
//
// Both slugs are in it so that two workspaces can hold a container of the same
// name in one namespace, which is the whole point of workspaces.
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

// selector renders labels as a kubectl -l argument, in a fixed order so the
// command line is stable and testable.
func selector(c model.Container) string {
	return fmt.Sprintf("%s=%s,%s=%s",
		labelWorkspace, slug(c.WorkspaceName),
		labelContainer, slug(c.Name))
}

// annotations record the names as typed, so `kubectl describe` still shows what
// the operator called things after slugging.
func annotations(c model.Container) map[string]string {
	return map[string]string{
		annWorkspace: c.WorkspaceName,
		annContainer: c.Name,
	}
}

// imageTag is where this container's image is pushed.
//
// The tag never changes, which is why the Deployment pulls Always and a rebuild
// bumps an annotation: otherwise the cluster keeps serving the image it already
// has under this name.
func imageTag(registry string, c model.Container) string {
	return fmt.Sprintf("%s/%s-%s:latest",
		strings.TrimSuffix(registry, "/"), slug(c.WorkspaceName), slug(c.Name))
}
