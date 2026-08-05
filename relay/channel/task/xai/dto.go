package xai

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

type pollResponse struct {
	Status string       `json:"status"`
	Model  string       `json:"model"`
	Video  *videoResult `json:"video,omitempty"`
	Error  *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
	// Some error paths return a bare reason string instead of the error object.
	Reason string `json:"reason,omitempty"`
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
