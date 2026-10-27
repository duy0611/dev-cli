package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
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

func TestLaunchAgentCallsOnStartBeforeExec(t *testing.T) {
	a := seedFake(t, model.StatusStopped)
	tg, err := a.resolve(t.Context(), "", "api")
	if err != nil {
		t.Fatal(err)
	}
	defer tg.release()
	ag, err := agent.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}

	err = a.launchAgent(t.Context(), tg, ag, []string{"claude", "x"}, launchOpts{
		event: "test",
		onStart: func() error {
			fake.log = append(fake.log, "onstart")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if fake.log[len(fake.log)-1] != "exec claude x" {
		t.Fatalf("last call = %q, want the exec", fake.log[len(fake.log)-1])
	}
	if fake.log[len(fake.log)-2] != "onstart" {
		t.Errorf("onStart did not run immediately before the exec: %v", fake.log)
	}
	if !slices.Contains(fake.log, "up") {
		t.Errorf("the container was not started: %v", fake.log)
	}
}

func TestLaunchAgentExecEnvIncludesOptsEnv(t *testing.T) {
	a := seedFake(t, model.StatusStopped)
	tg, err := a.resolve(t.Context(), "", "api")
	if err != nil {
		t.Fatal(err)
	}
	defer tg.release()
	ag, err := agent.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}

	lead := provider.EnvVar{Key: "DEV_TEAM_DISPATCH", Value: "/run/dev-team"}
	env := []provider.EnvVar{lead}
	err = a.launchAgent(t.Context(), tg, ag, []string{"claude", "x"}, launchOpts{
		event: "test",
		env:   env,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(fake.execs) == 0 {
		t.Fatal("the agent was never executed")
	}
	agentExec := fake.execs[len(fake.execs)-1]
	if !slices.Contains(agentExec.env, lead) {
		t.Errorf("the agent exec does not see %s: %v", lead.Key, agentExec.env)
	}
	if envHas(fake.upEnv, lead.Key) {
		t.Errorf("up saw the exec-only variable: %v", fake.upEnv)
	}
	if len(env) != 1 {
		t.Errorf("launchAgent changed the caller's env: %v", env)
	}
}
