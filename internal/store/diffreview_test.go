package store

import (
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// seedDiffContainer creates the workspace and the container the review rows
// hang off.
func seedDiffContainer(t *testing.T, s *Store) {
	t.Helper()
	seed(t, s)
	if err := s.CreateContainer(model.Container{
		Name: "api", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/src/api",
		Mode: model.ModeGuarded,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDiffReviewAbsentIsEmpty(t *testing.T) {
	s := openTest(t)
	seedDiffContainer(t, s)

	got, err := s.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatalf("GetDiffReview: %v", err)
	}
	if got.Draft != "" {
		t.Errorf("Draft = %q, want empty", got.Draft)
	}
	if !got.SentAt.IsZero() {
		t.Errorf("SentAt = %v, want zero", got.SentAt)
	}
	if got.WorkspaceName != "ws" || got.ContainerName != "api" {
		t.Errorf("names = %q/%q, want ws/api", got.WorkspaceName, got.ContainerName)
	}
}

func TestDiffDraftRoundTrip(t *testing.T) {
	s := openTest(t)
	seedDiffContainer(t, s)

	if err := s.SaveDiffDraft("ws", "api", "[1]"); err != nil {
		t.Fatalf("SaveDiffDraft: %v", err)
	}
	if err := s.SaveDiffDraft("ws", "api", "[2]"); err != nil {
		t.Fatalf("SaveDiffDraft (update): %v", err)
	}

	got, err := s.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatalf("GetDiffReview: %v", err)
	}
	if got.Draft != "[2]" {
		t.Errorf("Draft = %q, want [2]", got.Draft)
	}
}

func TestMarkDiffSentClearsTheDraft(t *testing.T) {
	s := openTest(t)
	seedDiffContainer(t, s)

	if err := s.SaveDiffDraft("ws", "api", "[1]"); err != nil {
		t.Fatalf("SaveDiffDraft: %v", err)
	}
	if err := s.MarkDiffSent("ws", "api", "[1]", "snap"); err != nil {
		t.Fatalf("MarkDiffSent: %v", err)
	}

	got, err := s.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatalf("GetDiffReview: %v", err)
	}
	if got.Draft != "" {
		t.Errorf("Draft = %q, want empty", got.Draft)
	}
	if got.Sent != "[1]" {
		t.Errorf("Sent = %q, want [1]", got.Sent)
	}
	if got.Snapshot != "snap" {
		t.Errorf("Snapshot = %q, want snap", got.Snapshot)
	}
	if got.SentAt.IsZero() {
		t.Error("SentAt is zero, want the send time")
	}

	if err := s.SaveDiffDraft("ws", "api", "[3]"); err != nil {
		t.Fatalf("SaveDiffDraft after send: %v", err)
	}
	got, err = s.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatalf("GetDiffReview: %v", err)
	}
	if got.Sent != "[1]" {
		t.Errorf("Sent = %q after a later draft, want [1]", got.Sent)
	}
}

func TestDiffReviewGoesWithTheContainer(t *testing.T) {
	s := openTest(t)
	seedDiffContainer(t, s)

	if err := s.SaveDiffDraft("ws", "api", "[1]"); err != nil {
		t.Fatalf("SaveDiffDraft: %v", err)
	}
	if err := s.DeleteContainer("ws", "api"); err != nil {
		t.Fatalf("DeleteContainer: %v", err)
	}
	if err := s.CreateContainer(model.Container{
		Name: "api", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/src/api",
		Mode: model.ModeGuarded,
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	got, err := s.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatalf("GetDiffReview: %v", err)
	}
	if got.Draft != "" {
		t.Errorf("Draft = %q, want empty after the container was removed", got.Draft)
	}
}
