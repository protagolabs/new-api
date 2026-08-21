package xai

import (
	"encoding/json"
	"testing"
)

// A string in this field used to fail the whole unmarshal, which left the task
// stuck: never terminal, retried on every polling cycle. The payload below is the
// shape that caused it in production.
func TestPollErrorAcceptsBothShapes(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantMessage string
		wantCode    string
		wantNil     bool
	}{
		{
			name:        "documented object shape",
			body:        `{"status":"failed","error":{"message":"content rejected","code":"moderation"}}`,
			wantMessage: "content rejected",
			wantCode:    "moderation",
		},
		{
			name:        "bare string shape",
			body:        `{"status":"failed","error":"internal error"}`,
			wantMessage: "internal error",
		},
		{
			name:    "explicit null",
			body:    `{"status":"processing","error":null}`,
			wantNil: true,
		},
		{
			name:    "field absent",
			body:    `{"status":"processing"}`,
			wantNil: true,
		},
		{
			name:        "empty string",
			body:        `{"status":"failed","error":""}`,
			wantMessage: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var poll pollResponse
			if err := json.Unmarshal([]byte(c.body), &poll); err != nil {
				t.Fatalf("unmarshal failed, the task would never reach a terminal state: %v", err)
			}
			if c.wantNil {
				if poll.Error != nil && poll.Error.Message != "" {
					t.Errorf("expected no error content, got %+v", *poll.Error)
				}
				return
			}
			if poll.Error == nil {
				t.Fatal("error object missing")
			}
			if poll.Error.Message != c.wantMessage {
				t.Errorf("message = %q, want %q", poll.Error.Message, c.wantMessage)
			}
			if poll.Error.Code != c.wantCode {
				t.Errorf("code = %q, want %q", poll.Error.Code, c.wantCode)
			}
		})
	}
}

// The rest of the response must still parse when the error field takes the
// string form -- otherwise a failed task loses its video url and usage.
func TestPollResponseIntactAlongsideStringError(t *testing.T) {
	body := `{"status":"failed","model":"grok-imagine-video","error":"upstream timeout",
	          "video":{"url":"https://example.invalid/v.mp4","duration":6},
	          "usage":{"cost_in_usd_ticks":500000000}}`
	var poll pollResponse
	if err := json.Unmarshal([]byte(body), &poll); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if poll.Status != "failed" || poll.Model != "grok-imagine-video" {
		t.Errorf("status/model lost: %q %q", poll.Status, poll.Model)
	}
	if poll.Error == nil || poll.Error.Message != "upstream timeout" {
		t.Errorf("error message lost: %+v", poll.Error)
	}
	if poll.Video == nil || poll.Video.URL == "" {
		t.Error("video result lost")
	}
	if poll.Usage == nil {
		t.Error("usage lost")
	}
}
