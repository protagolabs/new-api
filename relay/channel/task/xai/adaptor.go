package xai

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

const defaultBaseURL = "https://api.x.ai"

// Compile-time checks. OpenAIVideoConverter in particular is discovered by a
// runtime type assertion in videoFetchByIDRespBodyBuilder, so dropping the
// method would silently turn GET /v1/videos/{id} into a 501 instead of failing
// the build.
var (
	_ channel.TaskAdaptor          = (*TaskAdaptor)(nil)
	_ channel.OpenAIVideoConverter = (*TaskAdaptor)(nil)
)

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	apiKey      string
	baseURL     string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	if strings.TrimSpace(a.baseURL) == "" {
		a.baseURL = defaultBaseURL
	}
	a.apiKey = info.ApiKey
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	return relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionTextGenerate)
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/v1/videos/generations", strings.TrimRight(a.baseURL, "/")), nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

// taskRequest pulls the parsed submit request that ValidateRequestAndSetAction
// stashed in the context.
func taskRequest(c *gin.Context) (relaycommon.TaskSubmitReq, bool) {
	v, ok := c.Get("task_request")
	if !ok {
		return relaycommon.TaskSubmitReq{}, false
	}
	req, ok := v.(relaycommon.TaskSubmitReq)
	return req, ok
}

// resolveParams derives the effective request/billing parameters. Shared by
// BuildRequestBody and EstimateBilling so what we send upstream and what we
// charge for can never diverge.
func resolveParams(req relaycommon.TaskSubmitReq) (seconds int, resolution, aspect string, imageCount, inputVideoSeconds int) {
	var meta metadataParams
	_ = taskcommon.UnmarshalMetadata(req.Metadata, &meta)

	seconds = ResolveDuration(req.Metadata, req.Duration, req.Seconds)
	resolution = ResolveResolution(req.Metadata, req.Size)

	aspect = meta.AspectRatio
	if aspect == "" && req.Size != "" {
		aspect = SizeToAspectRatio(req.Size)
	}

	imageCount = len(req.Images)
	if imageCount == 0 && strings.TrimSpace(req.Image) != "" {
		imageCount = 1
	}
	inputVideoSeconds = meta.InputVideoSeconds
	return
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, ok := taskRequest(c)
	if !ok {
		return nil, errors.New("task_request not found in context")
	}

	seconds, resolution, aspect, _, _ := resolveParams(req)

	body := submitRequest{
		Model:       info.UpstreamModelName,
		Prompt:      req.Prompt,
		Duration:    seconds,
		Resolution:  resolution,
		AspectRatio: aspect,
	}
	if len(req.Images) > 0 && strings.TrimSpace(req.Images[0]) != "" {
		body.Image = &imageInput{URL: req.Images[0]}
	} else if strings.TrimSpace(req.Image) != "" {
		body.Image = &imageInput{URL: req.Image}
	}
	if body.Image != nil {
		info.Action = constant.TaskActionGenerate
	}

	data, err := common.Marshal(body)
	if err != nil {
		return nil, errors.Wrap(err, "marshal xai video request failed")
	}
	return bytes.NewReader(data), nil
}

// EstimateBilling charges the requested duration and resolution up front and
// folds the additive input-media cost into a multiplier. ModelPrice is the
// 480p per-second output price — see billing.go.
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	req, ok := taskRequest(c)
	if !ok {
		return nil
	}
	seconds, resolution, _, imageCount, inputVideoSeconds := resolveParams(req)
	return BillingRatios(info.OriginModelName, info.PriceData.ModelPrice, seconds, resolution,
		imageCount, inputVideoSeconds)
}

// SettlesPerCallOnComplete opts this adaptor into completion-time settlement
// even though the model is priced per call (ModelPrice). xAI bills video by the
// second actually delivered, so the pre-charged estimate must be reconciled.
// Implements service.PerCallSettlementAdaptor.
func (a *TaskAdaptor) SettlesPerCallOnComplete() bool { return true }

// AdjustBillingOnComplete re-prices the task against the duration xAI actually
// delivered. Returns 0 (keep the pre-charge) when the upstream reported no
// usable duration or it already matches the estimate.
func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil || taskResult.Status != model.TaskStatusSuccess {
		return 0
	}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.ModelPrice <= 0 {
		return 0
	}

	var poll pollResponse
	if len(task.Data) == 0 || common.Unmarshal(task.Data, &poll) != nil {
		return 0
	}

	groupRatio := bc.GroupRatio
	if groupRatio <= 0 {
		groupRatio = 1
	}

	// Preferred: xAI reports its own charge for the request. That is
	// authoritative — it already accounts for actual duration, resolution and
	// input media, and stays correct if xAI changes its rate card.
	if poll.Usage != nil {
		if cost := UpstreamCostUSD(poll.Usage.CostInUsdTicks); cost > 0 {
			return int(cost * common.QuotaPerUnit * groupRatio)
		}
	}

	// Fallback: re-price locally from the delivered duration.
	if poll.Video == nil {
		return 0
	}
	actual := CapDuration(poll.Video.Duration)
	if actual <= 0 {
		return 0
	}

	estimated := bc.OtherRatios["seconds"]
	if estimated == float64(actual) {
		return 0 // nothing to settle
	}

	resRatio := bc.OtherRatios["resolution"]
	if resRatio <= 0 {
		resRatio = 1
	}
	mediaRatio := bc.OtherRatios["media"]
	if mediaRatio <= 0 {
		mediaRatio = 1
	}

	// The media ratio was derived against the estimated duration, so recover the
	// absolute input cost before re-folding it over the actual duration.
	inputCost := (mediaRatio - 1) * bc.ModelPrice * estimated * resRatio
	total := bc.ModelPrice*float64(actual)*resRatio + inputCost
	if total <= 0 {
		return 0
	}

	return int(total * common.QuotaPerUnit * groupRatio)
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *taskdto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var s submitResponse
	if err := common.Unmarshal(responseBody, &s); err != nil {
		return "", nil, service.TaskErrorWrapper(err, "unmarshal_response_failed", http.StatusInternalServerError)
	}
	if s.Error != nil && s.Error.Message != "" {
		return "", nil, service.TaskErrorWrapper(errors.New(s.Error.Message), "upstream_error", http.StatusBadRequest)
	}
	if strings.TrimSpace(s.RequestID) == "" {
		return "", nil, service.TaskErrorWrapper(errors.New("missing request_id"), "invalid_response", http.StatusInternalServerError)
	}

	taskID := taskcommon.EncodeLocalTaskID(s.RequestID)

	ov := dto.NewOpenAIVideo()
	ov.ID = info.PublicTaskID
	ov.TaskID = info.PublicTaskID
	ov.CreatedAt = time.Now().Unix()
	ov.Model = info.OriginModelName
	c.JSON(http.StatusOK, ov)

	return taskID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, errors.New("invalid task_id")
	}
	upstreamID, err := taskcommon.DecodeLocalTaskID(taskID)
	if err != nil {
		return nil, errors.Wrap(err, "decode task_id failed")
	}
	if strings.TrimSpace(baseUrl) == "" {
		baseUrl = defaultBaseURL
	}

	url := fmt.Sprintf("%s/v1/videos/%s", strings.TrimRight(baseUrl, "/"), upstreamID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var poll pollResponse
	if err := common.Unmarshal(respBody, &poll); err != nil {
		return nil, errors.Wrap(err, "unmarshal xai video response failed")
	}

	ti := &relaycommon.TaskInfo{}

	reason := poll.Reason
	if poll.Error != nil && poll.Error.Message != "" {
		reason = poll.Error.Message
	}

	switch strings.ToLower(strings.TrimSpace(poll.Status)) {
	case "done", "succeeded", "success":
		ti.Status = model.TaskStatusSuccess
		ti.Progress = "100%"
		if poll.Video != nil && poll.Video.URL != "" {
			// Url is what the generic polling loop reads to decide the result
			// URL; leaving it empty makes it fall back to a locally built proxy
			// URL. RemoteUrl is only consumed by the Gemini video proxy, so
			// setting that alone (as the Veo adaptor does) is not enough here.
			ti.Url = poll.Video.URL
			ti.RemoteUrl = poll.Video.URL
		}
	case "failed", "expired", "cancelled", "canceled":
		ti.Status = model.TaskStatusFailure
		ti.Progress = "100%"
		if reason == "" {
			reason = "task " + poll.Status
		}
		ti.Reason = reason
	default:
		// processing / queued / anything unrecognised: keep polling rather than
		// settling, so a new upstream status can never silently fail a task.
		ti.Status = model.TaskStatusInProgress
		ti.Progress = "50%"
	}

	return ti, nil
}

// ConvertToOpenAIVideo renders the task for GET /v1/videos/{id}, the
// OpenAI-style fetch route. Without it that route answers
// "not_implemented:48" (HTTP 501) — the generic
// GET /v1/video/generations/{id} route is unaffected, so the gap only shows up
// for clients using the OpenAI video shape. Implements
// channel.OpenAIVideoConverter.
func (a *TaskAdaptor) ConvertToOpenAIVideo(originTask *model.Task) ([]byte, error) {
	if originTask == nil {
		return nil, errors.New("nil task")
	}

	video := dto.NewOpenAIVideo()
	video.ID = originTask.TaskID
	video.TaskID = originTask.TaskID
	video.Status = originTask.Status.ToVideoStatus()
	video.SetProgressStr(originTask.Progress)
	video.CreatedAt = originTask.CreatedAt
	video.CompletedAt = originTask.FinishTime
	video.Model = originTask.Properties.OriginModelName

	// task.Data holds the last poll response. It can legitimately be empty (task
	// still queued, or it failed before the first poll), so absence is not an
	// error — the status fields above already describe the task.
	var poll pollResponse
	if len(originTask.Data) > 0 && common.Unmarshal(originTask.Data, &poll) == nil {
		if poll.Video != nil {
			if poll.Video.URL != "" {
				video.SetMetadata("url", poll.Video.URL)
			}
			if poll.Video.Duration > 0 {
				video.Seconds = strconv.Itoa(poll.Video.Duration)
			}
		}
		if poll.Error != nil && poll.Error.Message != "" {
			video.Error = &dto.OpenAIVideoError{Message: poll.Error.Message, Code: poll.Error.Code}
		}
	}

	// Fall back to the reason the polling loop recorded when the upstream body
	// carried no error object (e.g. an "expired" status).
	if video.Error == nil && originTask.Status == model.TaskStatusFailure && originTask.FailReason != "" {
		video.Error = &dto.OpenAIVideoError{Message: originTask.FailReason}
	}

	return common.Marshal(video)
}

func (a *TaskAdaptor) GetModelList() []string {
	return []string{
		"grok-imagine-video",
		"grok-imagine-video-1.5",
	}
}

func (a *TaskAdaptor) GetChannelName() string {
	return "xai"
}
