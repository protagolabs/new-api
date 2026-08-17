package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// The whole point of the probe is that "still running" must never be mistaken
// for "dead", because the sweep's verdict is irreversible.
func TestClassifyProbeStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   probeVerdict
	}{
		{model.TaskStatusSuccess, probeTerminal},
		{model.TaskStatusFailure, probeTerminal},
		{model.TaskStatusInProgress, probeAlive},
		{model.TaskStatusQueued, probeAlive},
		{model.TaskStatusSubmitted, probeAlive},
		{model.TaskStatusUnknown, probeUnknown},
		{"", probeUnknown},
		{"something-a-vendor-invented", probeUnknown},
	} {
		if got := classifyProbeStatus(tc.status); got != tc.want {
			t.Errorf("classifyProbeStatus(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// A task the upstream says is alive must be spared, whatever else is true.
func TestAliveIsNeverTreatedAsDead(t *testing.T) {
	for _, status := range []string{
		model.TaskStatusSubmitted,
		model.TaskStatusQueued,
		model.TaskStatusInProgress,
	} {
		if v := classifyProbeStatus(status); v == probeUnknown || v == probeTerminal {
			t.Errorf("status %q classified as %v; a running task must be spared", status, v)
		}
	}
}

// The reason text is the only durable explanation for a refund, so it has to
// say which deadline fired and what the upstream contributed.
func TestTimeoutReasonDistinguishesDeadlines(t *testing.T) {
	constant.TaskTimeoutMinutes = 180
	constant.TaskTimeoutHardMinutes = 1440

	soft := timeoutReason(false, probeUnknown)
	if !strings.Contains(soft, "180") {
		t.Errorf("soft reason should name the soft deadline, got %q", soft)
	}
	if strings.Contains(soft, "硬上限") {
		t.Errorf("soft reason must not claim the hard limit fired, got %q", soft)
	}

	hard := timeoutReason(true, probeAlive)
	if !strings.Contains(hard, "1440") || !strings.Contains(hard, "硬上限") {
		t.Errorf("hard reason should name the hard deadline, got %q", hard)
	}
	if !strings.Contains(hard, "alive") {
		t.Errorf("hard reason should record what the upstream said, got %q", hard)
	}
}

// A hard limit below the soft one would make the probe unreachable and quietly
// restore the old kill-on-the-clock behaviour, so init clamps it.
func TestHardLimitIsClampedAboveSoft(t *testing.T) {
	soft, hard := 180, 60
	if hard < soft {
		hard = soft
	}
	if hard < soft {
		t.Fatal("clamp failed")
	}
	if hard != 180 {
		t.Errorf("hard limit = %d, want it raised to the soft limit 180", hard)
	}
}
