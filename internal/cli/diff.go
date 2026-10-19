package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/review"
	"github.com/spf13/cobra"
)

func newDiffCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Review a container's uncommitted changes with its agent",
		// RunE stops cobra inheriting the root's RunE, which exits 0 for a
		// mistyped subcommand.
		Args: noArgs(),
		RunE: groupRunE,
	}
	cmd.AddCommand(
		newDiffEditCmd(a),
		newDiffCompleteCmd(a),
	)
	return cmd
}

func newDiffEditCmd(a *app) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "edit NAME",
		Short: "Comment on a container's uncommitted changes",
		Long: "Open a container's uncommitted changes in your editor and write comments\n" +
			"under the lines they are about, with >> at the start of a line. Saving keeps\n" +
			"the comments as a draft; `dev diff complete` sends them to the agent.\n\n" +
			"Only a guarded container has its folder on the host, so only a guarded\n" +
			"container can be reviewed this way.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiffEdit(cmd.Context(), a, workspace, args[0])
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func newDiffCompleteCmd(a *app) *cobra.Command {
	var (
		workspace  string
		agentID    string
		newSession bool
		printMsg   bool
	)
	cmd := &cobra.Command{
		Use:   "complete NAME",
		Short: "Send the drafted comments to the agent",
		Long: "Send every comment from `dev diff edit` to the agent in one message, and mark\n" +
			"the round as sent so the next review shows what changed since. The agent has\n" +
			"to be installed in the container already; --print writes the message instead,\n" +
			"for an agent dev cannot start or a message that is too large.\n\n" +
			"The container is started first if it is not running.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiffComplete(cmd.Context(), a, workspace, args[0], completeOpts{
				agentID: agentID, newSession: newSession, print: printMsg,
			})
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&agentID, "agent", "",
		"which agent to run (claude, hermes, opencode); default claude")
	cmd.Flags().BoolVar(&newSession, "new-session", false,
		"start a new session instead of continuing the agent's last")
	cmd.Flags().BoolVar(&printMsg, "print", false,
		"print the message on stdout instead of running an agent")
	return cmd
}

// completeOpts are `diff complete`'s flags. An empty agentID means --agent was
// not given.
type completeOpts struct {
	agentID    string
	newSession bool
	print      bool
}

// diffFolders is the set of host folders a review reads: the container's
// source, then its extra folders.
func diffFolders(c model.Container) []review.Folder {
	var paths []string
	if c.SourceKind == model.SourceFolder && c.Source != "" {
		paths = append(paths, c.Source)
	}
	for _, f := range c.Folders {
		paths = append(paths, f.HostPath)
	}
	return review.Labels(paths)
}

// requireDiffMode refuses the modes whose changes are not on the host, or that
// have no folder at all.
func requireDiffMode(c model.Container) error {
	switch c.Mode {
	case model.ModeGuarded, model.ModeLegacy:
		return nil
	case model.ModeIsolated:
		// "is none" in the spec's sentence would read as a typo; the mode name
		// "folderless" is the plain-language one.
		return usageErrorf("dev diff works on guarded containers; %s is isolated", c.Name)
	case model.ModeNone:
		return usageErrorf("dev diff works on guarded containers; %s is folderless", c.Name)
	default:
		return usageErrorf("dev diff works on guarded containers; %s is %s", c.Name, c.Mode)
	}
}

func runDiffEdit(ctx context.Context, a *app, workspace, name string) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := requireDiffMode(t.container); err != nil {
		return err
	}

	d, err := review.Collect(ctx, diffFolders(t.container))
	if err != nil {
		return err
	}
	for _, skipped := range d.Skipped {
		a.printf("warning: %s is not a git repository; its changes are not shown\n", skipped)
	}
	if len(d.Files) == 0 {
		a.printf("no changes in %s\n", t.container.Name)
		return nil
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	dr, err := st.GetDiffReview(t.container.WorkspaceName, t.container.Name)
	if err != nil {
		return err
	}
	draft, err := review.DecodeComments(dr.Draft)
	if err != nil {
		return err
	}
	sent, err := review.DecodeComments(dr.Sent)
	if err != nil {
		return err
	}

	text := review.Render(d, review.Reanchor(d, draft), review.Previous{
		SentAt: dr.SentAt, Sent: sent, Snapshot: dr.Snapshot,
	})

	dir, err := os.MkdirTemp("", "dev-diff-")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, t.container.Name+".diff")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}

	for {
		if err := review.OpenEditor(ctx, path); err != nil {
			return fmt.Errorf("%v; the review is still at %s", err, path)
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		comments, err := review.ParseReview(d, string(saved))
		if err != nil {
			var edited *review.EditedError
			if !errors.As(err, &edited) {
				return err
			}
			if !isTerminal(os.Stdin) {
				return fmt.Errorf("%v; the review is still at %s", err, path)
			}
			if !confirm(os.Stdin, a.out, reopenPrompt(edited)) {
				return fmt.Errorf("%v; the review is still at %s", err, path)
			}
			continue
		}

		encoded, err := review.EncodeComments(comments)
		if err != nil {
			return err
		}
		if err := st.SaveDiffDraft(t.container.WorkspaceName, t.container.Name, encoded); err != nil {
			return err
		}
		_ = os.RemoveAll(dir)
		if string(saved) == text {
			a.printf("file unchanged; if your editor returned at once, set EDITOR=\"code --wait\"\n")
		}
		noun := "comments"
		if len(comments) == 1 {
			noun = "comment"
		}
		a.printf("%d %s saved; run dev diff complete %s to send them\n",
			len(comments), noun, t.container.Name)
		return nil
	}
}

// reopenPrompt asks about reopening after an edited diff, carrying the reason:
// an indented comment needs a different fix from a changed line.
func reopenPrompt(e *review.EditedError) string {
	return e.Error() + "; reopen the file?"
}

func runDiffComplete(ctx context.Context, a *app, workspace, name string, o completeOpts) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := requireDiffMode(t.container); err != nil {
		return err
	}
	if o.print && (o.agentID != "" || o.newSession) {
		return usageErrorf("--print writes the message itself; it cannot be combined with --agent or --new-session")
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	dr, err := st.GetDiffReview(t.container.WorkspaceName, t.container.Name)
	if err != nil {
		return err
	}
	draft, err := review.DecodeComments(dr.Draft)
	if err != nil {
		return err
	}
	if len(draft) == 0 {
		return usageErrorf("no comments to send; run dev diff edit %s first", t.container.Name)
	}

	d, err := review.Collect(ctx, diffFolders(t.container))
	if err != nil {
		return err
	}
	if stale := review.Stale(d, draft); len(stale) > 0 {
		var where []string
		for _, c := range stale {
			if c.Path != "" {
				where = append(where, fmt.Sprintf("%s:%d", c.Path, c.Line))
				continue
			}
			where = append(where, c.File)
		}
		return fmt.Errorf("the diff changed under these comments:\n  %s\nrun dev diff edit %s to re-anchor them",
			strings.Join(where, "\n  "), t.container.Name)
	}

	// Stale is empty here, so re-anchoring only refreshes each line number
	// from the hunk's current position; the stored one may predate an edit
	// that shifted it.
	msg := review.Message(t.container.Name, d, review.Reanchor(d, draft))
	if len(msg) > review.MessageLimit {
		return fmt.Errorf("the review is %d KiB, over the %d KiB one-message limit; send it with --print",
			len(msg)/1024, review.MessageLimit/1024)
	}
	snapshot := review.Snapshot(d)

	if o.print {
		a.printf("%s", msg)
		return st.MarkDiffSent(t.container.WorkspaceName, t.container.Name, dr.Draft, snapshot)
	}

	agentID := o.agentID
	if agentID == "" {
		agentID = "claude"
	}
	ag, err := agent.Lookup(agentID)
	if err != nil {
		return usageError(err)
	}
	argv, resumed, err := ag.PromptCommand(msg, !o.newSession)
	if err != nil {
		return usageErrorf("%v; use --print instead", err)
	}
	if !o.newSession && !resumed {
		a.printf("%s cannot continue a session here; starting a new one\n", ag.ID)
	}

	status, err := t.provider.Status(ctx, t.container)
	if err != nil {
		return err
	}
	if status == model.StatusRunning && agentRunning(ctx, t, ag) {
		if !confirm(os.Stdin, a.out,
			fmt.Sprintf("%s is already running in %s; start another anyway?", ag.ID, t.container.Name)) {
			return fmt.Errorf("%s is already running in %s; quit it first", ag.ID, t.container.Name)
		}
	}

	herdrName, err := herdrAgentName(t.container.Name, "")
	if err != nil {
		return err
	}
	if err := t.requireOverride(a); err != nil {
		return err
	}
	return a.launchAgent(ctx, t, ag, argv, launchOpts{
		herdrName: herdrName,
		event:     "diff-complete",
		onStart: func() error {
			return st.MarkDiffSent(t.container.WorkspaceName, t.container.Name, dr.Draft, snapshot)
		},
	})
}

// agentRunning reports whether the agent's binary is a running process in the
// container. pgrep may be absent from a minimal image, which counts as not
// running.
func agentRunning(ctx context.Context, t *target, ag agent.Agent) bool {
	err := t.provider.Exec(ctx, t.container,
		[]string{"sh", "-c", `command -v pgrep >/dev/null || exit 1; pgrep -x "$1" >/dev/null`, "sh", ag.Binary},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}
