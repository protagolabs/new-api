package xai

import (
	"bytes"
	"encoding/json"
)

// Request/response shapes for xAI's asynchronous video generation API.
//
//	POST /v1/videos/generations -> {"request_id": "..."}
//	GET  /v1/videos/{request_id} -> {"status": "...", "video": {...}}

type imageInput struct {
	URL string `json:"url"`
}

type submitRequest struct {
	Model       string      `json:"model"`
	Prompt      string      `json:"prompt,omitempty"`
	Duration    int         `json:"duration,omitempty"`
	AspectRatio string      `json:"aspect_ratio,omitempty"`
	Resolution  string      `json:"resolution,omitempty"`
	Image       *imageInput `json:"image,omitempty"`
}

type submitResponse struct {
	RequestID string `json:"request_id"`
	// xAI returns a structured error object rather than a bare message.
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type videoResult struct {
	URL string `json:"url"`
	// Duration is the actual generated length in seconds, used for
	// completion-time settlement against the pre-charged estimate.
	Duration int `json:"duration"`
}

// usageInfo carries xAI's own authoritative charge for the request.
// CostInUsdTicks is USD scaled by CostTicksPerUSD.
type usageInfo struct {
	CostInUsdTicks int64 `json:"cost_in_usd_ticks"`
}

type pollResponse struct {
	Status string       `json:"status"`
	Model  string       `json:"model"`
	Video  *videoResult `json:"video,omitempty"`
	Usage  *usageInfo   `json:"usage,omitempty"`
	Error  *pollError   `json:"error,omitempty"`
	// Some error paths return a bare reason string instead of the error object.
	Reason string `json:"reason,omitempty"`
}

// pollError accepts both shapes xAI puts in this field: the documented object,
// and a bare string.
//
// Declaring it as an object only is not a cosmetic problem. A string there makes
// the whole poll response fail to unmarshal, so the task never reaches a terminal
// state -- it is retried on every polling cycle indefinitely, and the caller sees
// a job that neither succeeds nor fails. Seen in production on a real task that
// retried every 45s until we noticed it in the logs.
type pollError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func (e *pollError) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		// A bare string carries no code, so it lands in Message where every
		// caller of this type already looks.
		return json.Unmarshal(trimmed, &e.Message)
	}
	// Alias to avoid recursing into this method.
	type plain pollError
	return json.Unmarshal(trimmed, (*plain)(e))
}

// metadataParams mirrors the fields callers may pass via `metadata`, which
// bypasses the standard TaskSubmitReq field validation.
type metadataParams struct {
	Duration    int    `json:"duration,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	// InputVideoSeconds lets a caller declare the length of an input video so it
	// can be billed; xAI charges per second of input video on grok-imagine-video.
	InputVideoSeconds int `json:"input_video_seconds,omitempty"`
}
