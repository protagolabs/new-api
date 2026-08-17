package service

import (
	"context"
	"fmt"
	"io"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// probeVerdict is what the upstream had to say about a task that has passed its
// soft timeout.
type probeVerdict int

const (
	// probeUnknown means we could not get an answer -- the channel is gone, the
	// request failed, or the response made no sense. We know nothing new.
	probeUnknown probeVerdict = iota
	// probeAlive means the upstream explicitly reported the task as still
	// running. Killing it now would discard work that may yet succeed.
	probeAlive
	// probeTerminal means the upstream reported success or failure. The regular
	// polling pass, which runs immediately after the sweep, will settle it
	// properly; the sweep must keep its hands off.
	probeTerminal
)

func (v probeVerdict) String() string {
	switch v {
	case probeAlive:
		return "alive"
	case probeTerminal:
		return "terminal"
	default:
		return "unknown"
	}
}

// probeUpstreamStatus asks the upstream what became of a task, without writing
// anything back.
//
// The sweep used to declare a task dead purely because enough wall-clock had
// passed, having never once asked the vendor. That is a guess presented as a
// fact, and it is unfalsifiable in the wrong direction: the sweep also sets
// progress to 100%, which drops the task out of the polling query for good, so
// a task killed while still running can never be revived. We have measured
// successful video tasks taking 43 minutes, so any fixed deadline will
// eventually cut one short.
//
// A nil TaskInfo with probeUnknown means "no answer" and leaves the decision to
// the caller's hard deadline.
func probeUpstreamStatus(ctx context.Context, task *model.Task) (probeVerdict, *relaycommon.TaskInfo) {
	if task == nil || GetTaskAdaptorFunc == nil {
		return probeUnknown, nil
	}
	// Suno and Midjourney have their own polling paths with different response
	// shapes; only the video adaptors share the FetchTask/ParseTaskResult pair
	// this probe relies on.
	if task.Platform == constant.TaskPlatformSuno || task.Platform == constant.TaskPlatformMidjourney {
		return probeUnknown, nil
	}
	upstreamID := task.GetUpstreamTaskID()
	if upstreamID == "" {
		// Nothing to ask about: the submit never returned an id.
		return probeUnknown, nil
	}
	adaptor := GetTaskAdaptorFunc(task.Platform)
	if adaptor == nil {
		return probeUnknown, nil
	}
	channel, err := model.CacheGetChannel(task.ChannelId)
	if err != nil {
		// A deleted channel is itself a decent reason to stop waiting, but say
		// "unknown" rather than "dead" and let the hard deadline decide.
		return probeUnknown, nil
	}

	baseURL := constant.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() != "" {
		baseURL = channel.GetBaseURL()
	}
	key := channel.Key
	if task.PrivateData.Key != "" {
		key = task.PrivateData.Key
	}

	info := &relaycommon.RelayInfo{}
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelBaseUrl: baseURL}
	info.ApiKey = key
	adaptor.Init(info)

	resp, err := adaptor.FetchTask(baseURL, key, map[string]any{
		"task_id": upstreamID,
		"action":  task.Action,
		"model":   task.Properties.OriginModelName,
	}, channel.GetSetting().Proxy)
	if err != nil {
		return probeUnknown, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return probeUnknown, nil
	}
	result, err := adaptor.ParseTaskResult(body)
	if err != nil || result == nil {
		return probeUnknown, nil
	}
	return classifyProbeStatus(result.Status), result
}

// classifyProbeStatus maps an upstream task status onto what the sweep is
// allowed to conclude from it.
func classifyProbeStatus(status string) probeVerdict {
	switch status {
	case model.TaskStatusSuccess, model.TaskStatusFailure:
		return probeTerminal
	case model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusInProgress:
		return probeAlive
	default:
		// Covers TaskStatusUnknown and the empty status an adaptor yields for
		// an error body -- both mean we learned nothing.
		return probeUnknown
	}
}

// timeoutReason spells out which deadline fired and what the upstream said, so
// a refunded task can be explained after the fact without digging through pod
// logs that have long since rotated.
func timeoutReason(hard bool, verdict probeVerdict) string {
	if hard {
		return fmt.Sprintf("任务超时（硬上限 %d 分钟，上游状态：%s）",
			constant.TaskTimeoutHardMinutes, verdict)
	}
	return fmt.Sprintf("任务超时（%d 分钟，上游无法确认任务状态）",
		constant.TaskTimeoutMinutes)
}
