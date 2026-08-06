package model

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// A downstream new-api polling this instance maps our status string back to a
// task status. Any value outside the OpenAI video enum is unmappable there and
// makes it fail the task, so every state a task can actually be observed in
// must map to a real enum value -- "unknown" is a bug, not a safe default.
func TestToVideoStatusCoversObservableStates(t *testing.T) {
	cases := map[TaskStatus]string{
		TaskStatusNotStart:   dto.VideoStatusQueued,
		TaskStatusSubmitted:  dto.VideoStatusQueued,
		TaskStatusQueued:     dto.VideoStatusQueued,
		TaskStatusInProgress: dto.VideoStatusInProgress,
		TaskStatusSuccess:    dto.VideoStatusCompleted,
		TaskStatusFailure:    dto.VideoStatusFailed,
	}
	for in, want := range cases {
		if got := in.ToVideoStatus(); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}

	// A genuinely unrecognised value still falls back rather than guessing.
	if got := TaskStatus("SOMETHING_NEW").ToVideoStatus(); got != dto.VideoStatusUnknown {
		t.Errorf("unrecognised status: got %q, want %q", got, dto.VideoStatusUnknown)
	}
}
