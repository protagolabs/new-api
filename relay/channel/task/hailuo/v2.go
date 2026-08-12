package hailuo

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/pkg/errors"
)

// MiniMax H3 speaks the vendor's v2 video API, which is a different protocol
// from the v1 endpoint the rest of this adaptor targets:
//
//   - submit is /v2/video_generation with a multimodal content[] array, not
//     /v1/video_generation with flat prompt/first_frame_image fields
//   - the query endpoint returns {"items":[...],"total":N} -- a list of recent
//     tasks -- rather than the single task that was asked for, so the entry has
//     to be picked out by id
//   - statuses are lowercase ("succeeded"), and the result URL is inline at
//     content.url with no file_id round trip
//   - usage reports the seconds actually billed
//
// Task adaptors are dispatched by channel type, so both API generations arrive
// here and are separated by model name.
const (
	v2SubmitEndpoint = "/v2/video_generation"
	v2QueryEndpoint  = "/v2/query/video_generation"

	V2Resolution768P = "768P"
	V2Resolution2K   = "2K"

	v2DefaultResolution = V2Resolution768P
	v2DefaultRatio      = "16:9"
	v2DefaultDuration   = 5

	V2MinDuration = 4
	V2MaxDuration = 15
)

// v2 task statuses, lowercase unlike v1's "Success"/"Fail".
const (
	v2StatusSucceeded = "succeeded"
	v2StatusFailed    = "failed"
)

// IsV2Model reports whether a model is served by the v2 API. Matching on the
// H3 prefix rather than an allowlist so variants (regeneration, dated builds)
// do not silently fall back to the v1 protocol and fail at request time.
func IsV2Model(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "minimax-h3")
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

type v2URLRef struct {
	URL string `json:"url"`
}

type v2ContentItem struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *v2URLRef `json:"image_url,omitempty"`
	VideoURL *v2URLRef `json:"video_url,omitempty"`
	AudioURL *v2URLRef `json:"audio_url,omitempty"`
}

type v2SubmitRequest struct {
	Model      string          `json:"model"`
	Content    []v2ContentItem `json:"content"`
	Duration   int             `json:"duration,omitempty"`
	Resolution string          `json:"resolution,omitempty"`
	Ratio      string          `json:"ratio,omitempty"`
}

type v2SubmitResponse struct {
	TaskID   string    `json:"task_id"`
	BaseResp *BaseResp `json:"base_resp,omitempty"`
}

type v2Usage struct {
	TotalSeconds  int `json:"total_seconds"`
	InputSeconds  int `json:"input_seconds"`
	OutputSeconds int `json:"output_seconds"`
}

type v2Item struct {
	ID         string    `json:"id"`
	Model      string    `json:"model"`
	Status     string    `json:"status"`
	CreatedAt  int64     `json:"created_at"`
	UpdatedAt  int64     `json:"updated_at"`
	Content    *v2URLRef `json:"content,omitempty"`
	Resolution string    `json:"resolution,omitempty"`
	Duration   int       `json:"duration,omitempty"`
	Ratio      string    `json:"ratio,omitempty"`
	TaskType   string    `json:"task_type,omitempty"`
	Usage      *v2Usage  `json:"usage,omitempty"`
	StatusMsg  string    `json:"status_msg,omitempty"`
}

type v2QueryResponse struct {
	Items    []v2Item  `json:"items"`
	Total    int       `json:"total"`
	BaseResp *BaseResp `json:"base_resp,omitempty"`
}

// v2Metadata carries the v2-only knobs a caller can pass through metadata.
type v2Metadata struct {
	Resolution string `json:"resolution"`
	Ratio      string `json:"ratio"`
	// AspectRatio is an alias for Ratio. The vendor calls the field "ratio",
	// but "aspect_ratio" is the common spelling elsewhere (and what the other
	// video adaptors here accept), so callers reach for it and would otherwise
	// have their choice silently dropped.
	AspectRatio string `json:"aspect_ratio"`
	// InputVideoSeconds lets a caller declare the length of a reference video
	// up front so the pre-charge covers it; the vendor bills input footage at
	// the output tier rate. The completion settlement corrects any mismatch.
	InputVideoSeconds int `json:"input_video_seconds"`
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

// buildV2Request maps the generic submit request onto the v2 wire format.
func buildV2Request(req *relaycommon.TaskSubmitReq, upstreamModel string) (*v2SubmitRequest, error) {
	var meta v2Metadata
	_ = taskcommon.UnmarshalMetadata(req.Metadata, &meta)

	seconds := v2ResolveDuration(req)
	resolution := v2ResolveResolution(meta.Resolution, req.Size)

	content := make([]v2ContentItem, 0, 2)
	// Reference media must precede the text item; the text item is required and
	// defines the motion.
	if img := v2FirstImage(req); img != "" {
		content = append(content, v2ContentItem{Type: "image_url", ImageURL: &v2URLRef{URL: img}})
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	content = append(content, v2ContentItem{Type: "text", Text: req.Prompt})

	out := &v2SubmitRequest{
		Model:      upstreamModel,
		Content:    content,
		Duration:   seconds,
		Resolution: resolution,
	}

	// ratio is required for text-to-video and rejected as "adaptive". With a
	// reference image the vendor derives it from the input, so sending one
	// would only risk conflicting with the image.
	if v2FirstImage(req) == "" {
		out.Ratio = taskcommon.DefaultString(
			taskcommon.DefaultString(meta.Ratio, meta.AspectRatio),
			v2ResolveRatio(req.Size))
	}
	return out, nil
}

func v2FirstImage(req *relaycommon.TaskSubmitReq) string {
	if len(req.Images) > 0 && strings.TrimSpace(req.Images[0]) != "" {
		return req.Images[0]
	}
	return strings.TrimSpace(req.Image)
}

// v2ResolveDuration clamps to the vendor's accepted range. An out-of-range
// value is a hard upstream rejection, and an unbounded one would also become an
// unbounded billing multiplier.
func v2ResolveDuration(req *relaycommon.TaskSubmitReq) int {
	seconds := v2DefaultDuration
	if req.Duration > 0 {
		seconds = req.Duration
	} else if req.Seconds != "" {
		if n, err := parseIntSafe(req.Seconds); err == nil && n > 0 {
			seconds = n
		}
	}
	return V2ClampDuration(seconds)
}

func V2ClampDuration(seconds int) int {
	if seconds < V2MinDuration {
		return V2MinDuration
	}
	if seconds > V2MaxDuration {
		return V2MaxDuration
	}
	return seconds
}

// V2ResolveResolution maps an explicit metadata value or an OpenAI-style size
// onto one of the two tiers the vendor prices differently.
func v2ResolveResolution(explicit, size string) string {
	if r := V2NormalizeResolution(explicit); r != "" {
		return r
	}
	if r := V2NormalizeResolution(size); r != "" {
		return r
	}
	// Height is the discriminator in a WxH size string.
	if _, h, ok := parseSize(size); ok {
		if h >= 1080 {
			return V2Resolution2K
		}
		return V2Resolution768P
	}
	return v2DefaultResolution
}

func V2NormalizeResolution(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "768P", "768":
		return V2Resolution768P
	case "2K", "1440P", "1440":
		return V2Resolution2K
	}
	return ""
}

func v2ResolveRatio(size string) string {
	w, h, ok := parseSize(size)
	if !ok || w <= 0 || h <= 0 {
		return v2DefaultRatio
	}
	if r := simplifyRatio(w, h); r != "" {
		return r
	}
	return v2DefaultRatio
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

// parseV2Result extracts our task from the list the query endpoint returns.
// wantID is required: the endpoint ignores the task_id query parameter and
// answers with recent tasks, so reading items[0] would report another task's
// status -- observed live, where a freshly submitted task's query returned only
// the previous task, already succeeded.
func parseV2Result(respBody []byte, wantID string) (*relaycommon.TaskInfo, error) {
	var resp v2QueryResponse
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, errors.Wrap(err, "unmarshal v2 task result failed")
	}

	info := &relaycommon.TaskInfo{}
	if resp.BaseResp != nil && resp.BaseResp.StatusCode != StatusSuccess {
		info.Code = resp.BaseResp.StatusCode
		info.Reason = resp.BaseResp.StatusMsg
		info.Status = model.TaskStatusFailure
		info.Progress = taskcommon.ProgressComplete
		return info, nil
	}

	item := v2FindItem(resp.Items, wantID)
	if item == nil {
		// Not in the returned window yet. Keep polling rather than failing a
		// task that is very likely still queued upstream.
		info.Status = model.TaskStatusInProgress
		info.Progress = taskcommon.ProgressQueued
		return info, nil
	}

	switch strings.ToLower(item.Status) {
	case v2StatusSucceeded:
		info.Status = model.TaskStatusSuccess
		info.Progress = taskcommon.ProgressComplete
		if item.Content != nil {
			// Url drives the generic polling loop's result-URL choice;
			// RemoteUrl is read by the video proxy. Leaving Url empty would
			// yield a locally built proxy URL instead of the vendor's.
			info.Url = item.Content.URL
			info.RemoteUrl = item.Content.URL
		}
	case v2StatusFailed:
		info.Status = model.TaskStatusFailure
		info.Progress = taskcommon.ProgressComplete
		info.Reason = taskcommon.DefaultString(item.StatusMsg, "task failed")
	default:
		// Unknown states keep polling: never fail a task the user already paid
		// for on a status string we simply do not recognise.
		info.Status = model.TaskStatusInProgress
		info.Progress = taskcommon.ProgressInProgress
	}
	return info, nil
}

func v2FindItem(items []v2Item, wantID string) *v2Item {
	if wantID == "" {
		// Without an id to match, only a single-entry list is unambiguous.
		if len(items) == 1 {
			return &items[0]
		}
		return nil
	}
	for i := range items {
		if items[i].ID == wantID {
			return &items[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Billing
// ---------------------------------------------------------------------------

// v2ResolutionRatios are multipliers over ModelPrice, which is configured as
// the 768P per-second rate. 2K is $0.13/s against 768P's $0.08/s.
var v2ResolutionRatios = map[string]float64{
	V2Resolution768P: 1.0,
	V2Resolution2K:   1.625,
}

func V2ResolutionRatio(resolution string) float64 {
	if r, ok := v2ResolutionRatios[V2NormalizeResolution(resolution)]; ok {
		return r
	}
	return 1.0
}

// V2BillingRatios prices a request as ModelPrice x seconds x resolution.
// Reference video footage is billed by the vendor at the output tier rate, so
// it is folded into the second count rather than a separate multiplier.
func V2BillingRatios(seconds int, resolution string, inputVideoSeconds int) map[string]float64 {
	billable := seconds
	if inputVideoSeconds > 0 {
		billable += inputVideoSeconds
	}
	return map[string]float64{
		"seconds":    float64(billable),
		"resolution": V2ResolutionRatio(resolution),
	}
}

// ---------------------------------------------------------------------------
// Small parsing helpers
// ---------------------------------------------------------------------------

func parseIntSafe(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

// parseSize reads a "WxH" size string, tolerating the unicode multiplication
// sign some clients send.
func parseSize(size string) (int, int, bool) {
	s := strings.ToLower(strings.TrimSpace(size))
	s = strings.ReplaceAll(s, "*", "x")
	s = strings.ReplaceAll(s, "\u00d7", "x")
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := parseIntSafe(parts[0])
	h, err2 := parseIntSafe(parts[1])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// simplifyRatio reduces WxH to the "16:9" form the vendor expects.
func simplifyRatio(w, h int) string {
	g := gcd(w, h)
	if g == 0 {
		return ""
	}
	return strconv.Itoa(w/g) + ":" + strconv.Itoa(h/g)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

// ---------------------------------------------------------------------------
// v2 adaptor methods
// ---------------------------------------------------------------------------

// doV2Response handles the v2 submit reply, which carries task_id at the top
// level and only includes base_resp when something went wrong.
func (a *TaskAdaptor) doV2Response(c *gin.Context, responseBody []byte, info *relaycommon.RelayInfo) (string, []byte, *taskdto.TaskError) {
	var resp v2SubmitResponse
	if err := common.Unmarshal(responseBody, &resp); err != nil {
		return "", nil, service.TaskErrorWrapper(
			errors.Wrapf(err, "body: %s", responseBody), "unmarshal_response_body_failed", http.StatusInternalServerError)
	}
	if resp.BaseResp != nil && resp.BaseResp.StatusCode != StatusSuccess {
		return "", nil, service.TaskErrorWrapper(
			errors.Errorf("minimax api error: %s", resp.BaseResp.StatusMsg),
			strconv.Itoa(resp.BaseResp.StatusCode), http.StatusBadRequest)
	}
	if strings.TrimSpace(resp.TaskID) == "" {
		return "", nil, service.TaskErrorWrapper(
			errors.Errorf("no task_id in response: %s", responseBody), "invalid_response", http.StatusInternalServerError)
	}

	ov := dto.NewOpenAIVideo()
	ov.ID = info.PublicTaskID
	ov.TaskID = info.PublicTaskID
	ov.CreatedAt = time.Now().Unix()
	ov.Model = info.OriginModelName
	c.JSON(http.StatusOK, ov)

	return resp.TaskID, responseBody, nil
}

// EstimateBilling pre-charges duration x resolution for H3, whose price varies
// by a factor of ~6 across the accepted range. v1 models keep the flat
// per-call ModelPrice they have always used.
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	if !IsV2Model(info.UpstreamModelName) {
		return nil
	}
	v, exists := c.Get("task_request")
	if !exists {
		return nil
	}
	req, ok := v.(relaycommon.TaskSubmitReq)
	if !ok {
		return nil
	}
	var meta v2Metadata
	_ = taskcommon.UnmarshalMetadata(req.Metadata, &meta)

	return V2BillingRatios(
		v2ResolveDuration(&req),
		v2ResolveResolution(meta.Resolution, req.Size),
		meta.InputVideoSeconds,
	)
}

// SettlesPerCallOnComplete opts into completion-time settlement despite
// ModelPrice being a per-call price. Harmless for v1 models: their
// AdjustBillingOnComplete returns 0, which leaves the pre-charge untouched.
// Implements service.PerCallSettlementAdaptor.
func (a *TaskAdaptor) SettlesPerCallOnComplete() bool { return true }

// AdjustBillingOnComplete re-prices against usage.total_seconds, the vendor's
// own billed figure, which already accounts for a shorter delivered clip and
// for reference footage billed at the output tier rate.
func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil || taskResult.Status != model.TaskStatusSuccess {
		return 0
	}
	if !IsV2Model(task.Properties.OriginModelName) && !IsV2Model(task.Properties.UpstreamModelName) {
		return 0
	}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.ModelPrice <= 0 {
		return 0
	}

	var resp v2QueryResponse
	if len(task.Data) == 0 || common.Unmarshal(task.Data, &resp) != nil {
		return 0
	}
	item := v2FindItem(resp.Items, task.GetUpstreamTaskID())
	if item == nil || item.Usage == nil {
		return 0
	}
	billed := item.Usage.TotalSeconds
	if billed <= 0 {
		billed = item.Usage.OutputSeconds
	}
	if billed <= 0 {
		return 0
	}

	groupRatio := bc.GroupRatio
	if groupRatio <= 0 {
		groupRatio = 1
	}
	// Prefer the resolution the vendor actually served over the requested one.
	resRatio := V2ResolutionRatio(item.Resolution)
	if item.Resolution == "" {
		if r := bc.OtherRatios["resolution"]; r > 0 {
			resRatio = r
		}
	}

	total := bc.ModelPrice * float64(V2ClampDuration(billed)) * resRatio
	if total <= 0 {
		return 0
	}
	return int(total * common.QuotaPerUnit * groupRatio)
}

// convertV2ToOpenAIVideo renders a v2 task for GET /v1/videos/{id}. The result
// URL is inline at content.url, and the entry has to be located by id because
// task.Data holds the whole recent-task list the query endpoint returned.
func convertV2ToOpenAIVideo(originTask *model.Task) ([]byte, error) {
	openAIVideo := originTask.ToOpenAIVideo()

	var resp v2QueryResponse
	// Empty or unparseable data is normal for a task that has not been polled
	// yet; the status fields on the task itself still describe it.
	if len(originTask.Data) > 0 && common.Unmarshal(originTask.Data, &resp) == nil {
		if item := v2FindItem(resp.Items, originTask.GetUpstreamTaskID()); item != nil {
			if item.Content != nil && item.Content.URL != "" {
				openAIVideo.SetMetadata("url", item.Content.URL)
			}
			if item.Duration > 0 {
				openAIVideo.Seconds = strconv.Itoa(item.Duration)
			}
			if item.Resolution != "" {
				openAIVideo.Size = item.Resolution
			}
			if strings.EqualFold(item.Status, v2StatusFailed) && item.StatusMsg != "" {
				openAIVideo.Error = &dto.OpenAIVideoError{Message: item.StatusMsg}
			}
		}
		if resp.BaseResp != nil && resp.BaseResp.StatusCode != StatusSuccess {
			openAIVideo.Error = &dto.OpenAIVideoError{
				Message: resp.BaseResp.StatusMsg,
				Code:    strconv.Itoa(resp.BaseResp.StatusCode),
			}
		}
	}

	if openAIVideo.Error == nil && originTask.Status == model.TaskStatusFailure && originTask.FailReason != "" {
		openAIVideo.Error = &dto.OpenAIVideoError{Message: originTask.FailReason}
	}

	jsonData, err := common.Marshal(openAIVideo)
	if err != nil {
		return nil, errors.Wrap(err, "marshal openai video failed")
	}
	return jsonData, nil
}
