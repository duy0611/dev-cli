package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

func TestStartAgentRefusesAnAgentNameHerdrWouldNot(t *testing.T) {
	a := seedFake(t, model.StatusStopped)
	err := runContainerStartAgent(t.Context(), a, "", "api", "claude", "Not.Valid", nil, false, nil)
	if err == nil || exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "--agent-name") {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
	if slices.Contains(fake.log, "up") {
		t.Errorf("started the container before refusing: %v", fake.log)
	}
}

func TestHerdrAgentName(t *testing.T) {
	for _, c := range []struct{ container, flag, want string }{
		{"Fix.Header", "", "fix-header"},
		{"Fix.Header", "reviewer", "reviewer"},
		{"2fa", "", ""},
		{"2fa", "auth", "auth"},
	} {
		got, err := herdrAgentName(c.container, c.flag)
		if err != nil || got != c.want {
			t.Errorf("herdrAgentName(%q, %q) = %q, %v; want %q", c.container, c.flag, got, err, c.want)
		}
	}
}
