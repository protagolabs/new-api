package sora

import "testing"

// OpenAI's Sora exposes no direct URL, so Url must stay empty and the caller
// falls back to a local proxy URL. A nested new-api upstream does report one in
// metadata.url, and passing it through keeps the vendor's address end to end
// instead of proxying the bytes through this instance.
func TestParseTaskResultDirectURLPassthrough(t *testing.T) {
	a := &TaskAdaptor{}

	cases := []struct {
		name    string
		body    string
		wantURL string
	}{
		{
			name:    "openai sora has no metadata",
			body:    `{"id":"v1","status":"completed","progress":100}`,
			wantURL: "",
		},
		{
			name:    "nested new-api reports the vendor url",
			body:    `{"id":"v1","status":"completed","progress":100,"metadata":{"url":"https://vidgen.x.ai/a.mp4"}}`,
			wantURL: "https://vidgen.x.ai/a.mp4",
		},
		{
			// Anything that is not an absolute http(s) URL must be ignored, so a
			// relative or data: value cannot be handed to the caller as a result.
			name:    "non-absolute metadata url ignored",
			body:    `{"id":"v1","status":"completed","metadata":{"url":"/v1/videos/x/content"}}`,
			wantURL: "",
		},
		{
			name:    "non-string metadata url ignored",
			body:    `{"id":"v1","status":"completed","metadata":{"url":123}}`,
			wantURL: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ti, err := a.ParseTaskResult([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if ti.Url != tc.wantURL {
				t.Errorf("Url: got %q, want %q", ti.Url, tc.wantURL)
			}
		})
	}

	// A direct URL on a non-terminal status must not leak into the result.
	ti, err := a.ParseTaskResult([]byte(
		`{"id":"v1","status":"in_progress","metadata":{"url":"https://vidgen.x.ai/a.mp4"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ti.Url != "" {
		t.Errorf("in_progress must not carry a result URL, got %q", ti.Url)
	}
}
