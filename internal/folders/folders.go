// Package folders decides where each of a container's host folders appears
// inside it, and which sets of folders are refused.
package folders

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/duy0611/dev-cli/internal/xpath"
)

// Entry is one folder of a container as validation sees it. The first entry
// given to Check is the primary.
type Entry struct {
	// Host is the folder, resolved.
	Host string
	// Extent is the host directory the container receives for it: Host, or the
	// whole git root for a guarded primary.
	Extent string
	// Container is where it appears inside the container.
	Container string
}

// ContainerPath is where a folder beyond the primary appears: a guarded
// worktree checkout at its own host path (invariant 11), anything else at
// /workspaces/<basename>.
func ContainerPath(host string, worktree, isolated bool) string {
	if worktree && !isolated {
		return host
	}
	// posix: the path is inside a Linux container whatever the host is.
	return path.Join("/workspaces", filepath.Base(host))
}

// Check refuses a folder given twice, a folder inside another's extent, and two
// folders that would appear at one container path.
func Check(entries []Entry) error {
	for i, a := range entries {
		for _, b := range entries[i+1:] {
			if err := pair(a, b); err != nil {
				return err
			}
		}
	}
	return nil
}

func pair(a, b Entry) error {
	switch {
	case a.Host == b.Host:
		return fmt.Errorf("%s is given twice", xpath.Shorten(a.Host))
	case within(b.Host, a.Extent):
		return nested(b, a)
	case within(a.Host, b.Extent):
		return nested(a, b)
	case a.Container == b.Container:
		return fmt.Errorf("%s and %s would both appear at %s in the container; "+
			"rename one of the folders, or leave one out",
			xpath.Shorten(a.Host), xpath.Shorten(b.Host), a.Container)
	}
	return nil
}

func nested(inner, outer Entry) error {
	if outer.Extent != outer.Host {
		return fmt.Errorf("%s is inside %s, the repository %s is mounted with; "+
			"it is already in the container",
			xpath.Shorten(inner.Host), xpath.Shorten(outer.Extent), xpath.Shorten(outer.Host))
	}
	return fmt.Errorf("%s is inside %s; a folder inside another would be in the container twice",
		xpath.Shorten(inner.Host), xpath.Shorten(outer.Host))
}

// within reports whether p is dir or lies below it, by path segment.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}
