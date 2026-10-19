package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/review"
)

// diffRepo is a repository whose a.go has one uncommitted added line.
func diffRepo(t *testing.T) string {
	t.Helper()
	repo := initRepo(t)
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n\nfunc f() {}\n")
	gitIn(t, repo, "add", "a.go")
	gitIn(t, repo, "commit", "-qm", "a")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n\n// added\nfunc f() {}\n")
	return repo
}

// seedDiffContainer makes api a container on a repository with one
// uncommitted change, in the given mode.
func seedDiffContainer(t *testing.T, mode model.Mode) *app {
	t.Helper()
	requireGit(t)
	a := seedFake(t, model.StatusStopped)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	c.Source = diffRepo(t)
	c.SourceKind = model.SourceFolder
	c.Mode = mode
	if err := st.DeleteContainer("ws", "api"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	return a
}

func appOut(a *app) *bytes.Buffer {
	return a.out.(*bytes.Buffer)
}

// stubEditorScript points VISUAL at a shell script and clears EDITOR.
func stubEditorScript(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", path)
	t.Setenv("EDITOR", "")
}

// stubEditor rewrites the review file in place with an awk program, so it
// behaves the same on macOS.
func stubEditor(t *testing.T, program string) {
	t.Helper()
	stubEditorScript(t, `awk '`+program+`' "$1" > "$1.tmp" && mv "$1.tmp" "$1"`)
}

// stubEditorCapturing copies the file it was given to dest, then edits it.
func stubEditorCapturing(t *testing.T, program, dest string) {
	t.Helper()
	stubEditorScript(t, `cp "$1" '`+dest+`'`+"\n"+
		`awk '`+program+`' "$1" > "$1.tmp" && mv "$1.tmp" "$1"`)
}

// addCommentAwk puts ">> why?" under the first added line.
const addCommentAwk = `{ print; if (/^\+/ && !/^\+\+\+/ && !done) { print ">> why?"; done = 1 } }`

// tamperAwk changes the diff itself rather than adding a comment.
const tamperAwk = `{ print; if (/^\+/ && !/^\+\+\+/ && !done) { print "+tampered"; done = 1 } }`

func editWithComment(t *testing.T, a *app) {
	t.Helper()
	stubEditor(t, addCommentAwk)
	if err := runDiffEdit(t.Context(), a, "", "api"); err != nil {
		t.Fatalf("diff edit: %v", err)
	}
}

func lastFakeCall(t *testing.T) string {
	t.Helper()
	if len(fake.log) == 0 {
		t.Fatal("the provider was not called")
	}
	return fake.log[len(fake.log)-1]
}

func TestDiffEditSavesComments(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	dr, err := st.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	comments, err := review.DecodeComments(dr.Draft)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("%d comments saved, want 1", len(comments))
	}
	if comments[0].Path != "a.go" || comments[0].Body != "why?" {
		t.Errorf("comment = %+v, want a.go: why?", comments[0])
	}
}

func TestDiffEditNoChanges(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, c.Source, "add", "a.go")
	gitIn(t, c.Source, "commit", "-qm", "clean")

	sentinel := filepath.Join(t.TempDir(), "ran")
	stubEditorScript(t, "touch '"+sentinel+"'")

	if err := runDiffEdit(t.Context(), a, "", "api"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("the editor ran with nothing to review")
	}
	if !strings.Contains(appOut(a).String(), "no changes in api") {
		t.Errorf("output = %q", appOut(a).String())
	}
}

func TestDiffEditRefusesOtherModes(t *testing.T) {
	for _, tc := range []struct {
		mode model.Mode
		want string
	}{
		{model.ModeIsolated, "is isolated"},
		{model.ModeNone, "is folderless"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			a := seedDiffContainer(t, tc.mode)
			stubEditorScript(t, "true")

			err := runDiffEdit(t.Context(), a, "", "api")
			if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v (exit %d), want %q", err, exitCodeOf(err), tc.want)
			}
		})
	}
}

func TestDiffEditEditedLineNoTerminal(t *testing.T) {
	noTerminal(t)
	a := seedDiffContainer(t, model.ModeGuarded)
	stubEditor(t, tamperAwk)

	err := runDiffEdit(t.Context(), a, "", "api")
	if exitCodeOf(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1", err, exitCodeOf(err))
	}
	if !strings.Contains(err.Error(), "line ") {
		t.Errorf("error does not name the line: %v", err)
	}
	kept := false
	for _, field := range strings.Fields(err.Error()) {
		if _, statErr := os.Stat(field); statErr == nil {
			kept = true
		}
	}
	if !kept {
		t.Errorf("error does not name a file that still exists: %v", err)
	}

	st, _ := a.store()
	dr, err := st.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	if dr.Draft != "" {
		t.Errorf("a draft was saved from an edited diff: %s", dr.Draft)
	}
}

func TestDiffEditKeepsTheDraftAcrossRuns(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)

	captured := filepath.Join(t.TempDir(), "captured.diff")
	stubEditorCapturing(t, `{ print }`, captured)
	if err := runDiffEdit(t.Context(), a, "", "api"); err != nil {
		t.Fatalf("second edit: %v", err)
	}
	b, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), ">> why?") {
		t.Errorf("the second run lost the draft comment:\n%s", b)
	}
}

func TestDiffCompletePrint(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)

	out := appOut(a)
	out.Reset()
	if err := runDiffComplete(t.Context(), a, "", "api", completeOpts{print: true}); err != nil {
		t.Fatalf("diff complete: %v", err)
	}
	if !strings.Contains(out.String(), "why?") {
		t.Errorf("the message does not carry the comment:\n%s", out.String())
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	dr, err := st.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	if dr.Draft != "" || dr.Sent == "" {
		t.Errorf("after printing: draft %q, sent %q", dr.Draft, dr.Sent)
	}

	captured := filepath.Join(t.TempDir(), "captured.diff")
	stubEditorCapturing(t, `{ print }`, captured)
	if err := runDiffEdit(t.Context(), a, "", "api"); err != nil {
		t.Fatalf("edit after sending: %v", err)
	}
	b, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "# Last review") {
		t.Errorf("the next review does not show the last round:\n%s", b)
	}
}

func TestDiffCompleteNoDraft(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	err := runDiffComplete(t.Context(), a, "", "api", completeOpts{})
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "run dev diff edit api first") {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
}

func TestDiffCompletePrintConflicts(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	err := runDiffComplete(t.Context(), a, "", "api", completeOpts{print: true, agentID: "claude"})
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "--print") {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
}

func TestDiffCompleteLaunchesTheAgent(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)

	if err := runDiffComplete(t.Context(), a, "", "api", completeOpts{}); err != nil {
		t.Fatalf("diff complete: %v", err)
	}
	last := lastFakeCall(t)
	if !strings.HasPrefix(last, "exec claude --continue ") {
		t.Errorf("last call = %q, want claude resuming with the review", last)
	}
	if !strings.Contains(last, "why?") {
		t.Error("the agent did not receive the comment")
	}

	st, _ := a.store()
	dr, err := st.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	if dr.Draft != "" || dr.Sent == "" {
		t.Errorf("after sending: draft %q, sent %q", dr.Draft, dr.Sent)
	}
}

func TestDiffCompleteNewSession(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)

	if err := runDiffComplete(t.Context(), a, "", "api", completeOpts{newSession: true}); err != nil {
		t.Fatalf("diff complete: %v", err)
	}
	last := lastFakeCall(t)
	if !strings.HasPrefix(last, "exec claude These are my review comments") {
		t.Errorf("last call = %q, want a new claude session", last)
	}
}

func TestDiffCompleteAgentArgv(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"opencode", "exec opencode --continue --prompt These are my review comments"},
		{"hermes", "exec hermes chat --continue --query These are my review comments"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			a := seedDiffContainer(t, model.ModeGuarded)
			editWithComment(t, a)
			if err := runDiffComplete(t.Context(), a, "", "api", completeOpts{agentID: tc.id}); err != nil {
				t.Fatalf("diff complete: %v", err)
			}
			if last := lastFakeCall(t); !strings.HasPrefix(last, tc.want) {
				t.Errorf("last call = %q, want prefix %q", last, tc.want)
			}
		})
	}
}

func TestDiffCompleteRefusesCodex(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)

	err := runDiffComplete(t.Context(), a, "", "api", completeOpts{agentID: "codex"})
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "--print") {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
	for _, call := range fake.log {
		if strings.HasPrefix(call, "exec codex") {
			t.Errorf("codex was run: %q", call)
		}
	}
	st, _ := a.store()
	dr, err := st.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	if dr.Draft == "" {
		t.Error("the draft was cleared by a refused send")
	}
}

func TestDiffCompleteStale(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(c.Source, "a.go"), "package a\n\n// changed\nfunc f() {}\n")

	err = runDiffComplete(t.Context(), a, "", "api", completeOpts{})
	if exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "a.go") {
		t.Errorf("err = %v (exit %d), want a stale a.go", err, exitCodeOf(err))
	}
	if !strings.Contains(err.Error(), "re-anchor") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
	dr, err := st.GetDiffReview("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	if dr.Draft == "" {
		t.Error("a stale review was cleared")
	}
}

func TestDiffCompleteAgentAlreadyRunning(t *testing.T) {
	noTerminal(t)
	a := seedDiffContainer(t, model.ModeGuarded)
	editWithComment(t, a)
	fake.status = model.StatusRunning
	fake.pgrepRunning = true

	err := runDiffComplete(t.Context(), a, "", "api", completeOpts{})
	if exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "quit it first") {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
	for _, call := range fake.log {
		if strings.HasPrefix(call, "exec claude") {
			t.Errorf("a second agent was started: %q", call)
		}
	}
}

func TestDiffCompleteTooLarge(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	draft, err := review.EncodeComments([]review.Comment{{Body: strings.Repeat("x", 101<<10)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveDiffDraft("ws", "api", draft); err != nil {
		t.Fatal(err)
	}

	err = runDiffComplete(t.Context(), a, "", "api", completeOpts{})
	if exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "--print") {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
}

// The hunk is the anchor; its line numbers are display data, so a stored line
// that has gone stale is refreshed rather than shown.
func TestDiffCompleteRefreshesLineNumbers(t *testing.T) {
	a := seedDiffContainer(t, model.ModeGuarded)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	d, err := review.Collect(t.Context(), diffFolders(c))
	if err != nil {
		t.Fatal(err)
	}
	f := d.Files[0]
	h := f.Hunks[0]
	draft, err := review.EncodeComments([]review.Comment{{
		Anchor: review.Anchor{
			File: f.Key(), Path: f.Path, Hunk: h.Body(), Occurrence: 0,
			Index: 2, Kind: review.Added, Line: 999, Text: "// added",
		},
		Body: "why?",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveDiffDraft("ws", "api", draft); err != nil {
		t.Fatal(err)
	}

	out := appOut(a)
	out.Reset()
	if err := runDiffComplete(t.Context(), a, "", "api", completeOpts{print: true}); err != nil {
		t.Fatalf("diff complete: %v", err)
	}
	if strings.Contains(out.String(), "999") {
		t.Errorf("the message used the stored line number:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "## 1. a.go:3 (added line)") {
		t.Errorf("the message does not use the current line number:\n%s", out.String())
	}
}

func TestReopenPromptKeepsTheReason(t *testing.T) {
	prompt := reopenPrompt(&review.EditedError{Line: 7, Indented: true})
	if !strings.Contains(prompt, "line 7") || !strings.Contains(prompt, "column 0") {
		t.Errorf("prompt = %q, want the line and the column-0 explanation", prompt)
	}
}
