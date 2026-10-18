package folders

import (
	"strings"
	"testing"
)

func TestContainerPath(t *testing.T) {
	for _, tc := range []struct {
		host               string
		worktree, isolated bool
		want               string
	}{
		{"/src/api", false, false, "/workspaces/api"},
		{"/src/api", false, true, "/workspaces/api"},
		{"/wt/feat", true, false, "/wt/feat"},
		// Copied, never mounted, so nothing pins it to its host path.
		{"/wt/feat", true, true, "/workspaces/feat"},
	} {
		if got := ContainerPath(tc.host, tc.worktree, tc.isolated); got != tc.want {
			t.Errorf("ContainerPath(%q, %v, %v) = %q, want %q", tc.host, tc.worktree, tc.isolated, got, tc.want)
		}
	}
}

func TestCheckAcceptsSeparateFolders(t *testing.T) {
	if err := Check([]Entry{
		{Host: "/w/repo-1", Extent: "/w/repo-1", Container: "/workspaces/repo-1"},
		{Host: "/w/repo-2", Extent: "/w/repo-2", Container: "/workspaces/repo-2"},
		{Host: "/w/repo-10", Extent: "/w/repo-10", Container: "/workspaces/repo-10"},
	}); err != nil {
		t.Errorf("Check = %v, want nil", err)
	}
}

func TestCheckRefuses(t *testing.T) {
	for label, tc := range map[string]struct {
		entries []Entry
		names   []string
	}{
		"a folder twice": {
			entries: []Entry{
				{Host: "/w/a", Extent: "/w/a", Container: "/workspaces/a"},
				{Host: "/w/a", Extent: "/w/a", Container: "/workspaces/a"},
			},
			names: []string{"/w/a"},
		},
		"a folder inside another": {
			entries: []Entry{
				{Host: "/w", Extent: "/w", Container: "/workspaces/w"},
				{Host: "/w/a", Extent: "/w/a", Container: "/workspaces/a"},
			},
			names: []string{"w/a", "w"},
		},
		"a folder elsewhere in the primary's repository": {
			entries: []Entry{
				{Host: "/w/repo/svc", Extent: "/w/repo", Container: "/workspaces/repo"},
				{Host: "/w/repo/lib", Extent: "/w/repo/lib", Container: "/workspaces/lib"},
			},
			names: []string{"repo/lib", "repo/svc"},
		},
		"two extras with one name": {
			entries: []Entry{
				{Host: "/w/p", Extent: "/w/p", Container: "/workspaces/p"},
				{Host: "/a/api", Extent: "/a/api", Container: "/workspaces/api"},
				{Host: "/b/api", Extent: "/b/api", Container: "/workspaces/api"},
			},
			names: []string{"a/api", "b/api", "/workspaces/api"},
		},
		"an extra named like the primary's mount": {
			entries: []Entry{
				{Host: "/x/repo/svc", Extent: "/x/repo", Container: "/workspaces/repo"},
				{Host: "/y/repo", Extent: "/y/repo", Container: "/workspaces/repo"},
			},
			names: []string{"x/repo/svc", "y/repo"},
		},
	} {
		t.Run(label, func(t *testing.T) {
			err := Check(tc.entries)
			if err == nil {
				t.Fatal("Check = nil, want a refusal")
			}
			for _, n := range tc.names {
				if !strings.Contains(err.Error(), n) {
					t.Errorf("error %q does not name %s", err, n)
				}
			}
		})
	}
}
