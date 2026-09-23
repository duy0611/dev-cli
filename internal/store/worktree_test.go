package store

import (
	"errors"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// seedWorktreeContainer gives a worktree row something to hang off, since the
// foreign key is what the cascade depends on. Built on the package's own seed,
// which already creates the provider and workspace.
func seedWorktreeContainer(t *testing.T, s *Store) {
	t.Helper()
	seed(t, s)
	if err := s.CreateContainer(model.Container{
		Name: "feat", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/wt/feat",
	}); err != nil {
		t.Fatal(err)
	}
}

func testWorktree() model.Worktree {
	return model.Worktree{
		WorkspaceName:  "ws",
		ContainerName:  "feat",
		Repo:           "/src/app/.git",
		Branch:         "feat",
		Path:           "/wt/feat",
		HerdrWorkspace: "7f2a",
	}
}

func TestWorktreeRoundTrip(t *testing.T) {
	s := openTest(t)
	seedWorktreeContainer(t, s)

	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	got, err := s.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatalf("GetWorktree: %v", err)
	}
	if got.Repo != "/src/app/.git" || got.Branch != "feat" ||
		got.Path != "/wt/feat" || got.HerdrWorkspace != "7f2a" {
		t.Errorf("GetWorktree = %+v, want the seeded row", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero; it is written by the application")
	}
}

func TestGetWorktreeMissing(t *testing.T) {
	s := openTest(t)
	seedWorktreeContainer(t, s)

	_, err := s.GetWorktree("ws", "feat")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The cascade is what makes `container remove` safe to leave alone: it can
// never leave a row describing a container that is gone. It depends on
// PRAGMA foreign_keys = ON, which is invariant 6.
func TestDeletingTheContainerTakesTheWorktree(t *testing.T) {
	s := openTest(t)
	seedWorktreeContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteContainer("ws", "feat"); err != nil {
		t.Fatalf("DeleteContainer: %v", err)
	}
	if _, err := s.GetWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("worktree survived the container: %v", err)
	}
}

// And so does deleting the workspace, two cascades deep.
func TestDeletingTheWorkspaceTakesTheWorktree(t *testing.T) {
	s := openTest(t)
	seedWorktreeContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteWorkspace("ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := s.GetWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("worktree survived the workspace: %v", err)
	}
}

func TestListWorktrees(t *testing.T) {
	s := openTest(t)
	seedWorktreeContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListWorktrees("")
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListWorktrees returned %d rows, want 1", len(all))
	}
	scoped, err := s.ListWorktrees("ws")
	if err != nil {
		t.Fatalf("ListWorktrees(ws): %v", err)
	}
	if len(scoped) != 1 {
		t.Errorf("ListWorktrees(ws) returned %d rows, want 1", len(scoped))
	}
	none, err := s.ListWorktrees("other")
	if err != nil {
		t.Fatalf("ListWorktrees(other): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListWorktrees(other) returned %d rows, want 0", len(none))
	}
}

func TestDeleteWorktreeMissing(t *testing.T) {
	s := openTest(t)
	seedWorktreeContainer(t, s)

	if err := s.DeleteWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
