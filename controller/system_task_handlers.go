package controller

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const (
	maxScheduledTaskIntervalMinutes = 7 * 24 * 60
	defaultChannelTestInterval      = 10 * time.Minute
)

// RegisterScheduledSystemTasks wires the periodic channel test, upstream model
// update, and async task polling (Midjourney / Suno / video) jobs into the
// system task framework so a DB lease dedups execution across multiple master
// instances and each run is recorded as one task row. Call this before
// service.StartSystemTaskRunner.
func RegisterScheduledSystemTasks() {
	service.RegisterSystemTaskHandler(channelTestHandler{})
	service.RegisterSystemTaskHandler(modelUpdateHandler{})
	service.RegisterSystemTaskHandler(midjourneyPollHandler{})
	service.RegisterSystemTaskHandler(asyncTaskPollHandler{})
}

// channelTestHandler runs the scheduled "test all channels" job. Enablement and
// cadence still come from the monitor settings; only the execution path moved
// into the system task runner.
type channelTestHandler struct{}

func (channelTestHandler) Type() string { return model.SystemTaskTypeChannelTest }

func (channelTestHandler) Enabled() bool {
	return operation_setting.GetMonitorSettingSnapshot().AutoTestChannelEnabled
}

func (channelTestHandler) Interval() time.Duration {
	minutes := operation_setting.GetMonitorSettingSnapshot().AutoTestChannelMinutes
	// The setting is a float for backwards compatibility, but NaN/Inf and
	// values that round to a sub-millisecond duration can make the scheduler
	// spin or overflow when converted to time.Duration. Keep a practical lower
	// bound and a finite upper bound at the final scheduling boundary.
	maxMinutes := float64(maxScheduledTaskIntervalMinutes)
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < 1 || minutes > maxMinutes {
		return defaultChannelTestInterval
	}
	durationNanos := minutes * float64(time.Minute)
	if math.IsNaN(durationNanos) || math.IsInf(durationNanos, 0) ||
		durationNanos < float64(time.Minute) || durationNanos > float64(maxScheduledTaskIntervalMinutes)*float64(time.Minute) {
		return defaultChannelTestInterval
	}
	duration := time.Duration(durationNanos)
	if duration <= 0 {
		return defaultChannelTestInterval
	}
	return duration
}

func (channelTestHandler) NewPayload() any { return nil }

// channelTestTaskPayload controls one channel_test run. A nil/empty payload is a
// scheduled run, which uses the configured monitor ChannelTestMode and does not
// notify. A manual "test all channels" trigger sets Mode=scheduled_all and
// Notify=true to reproduce the legacy manual behavior (test every channel and
// notify root on completion).
type channelTestTaskPayload struct {
	Mode   string `json:"mode,omitempty"`
	Notify bool   `json:"notify,omitempty"`
}

func (channelTestHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := channelTestTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary, err := runChannelTestTask(ctx, payload.Mode, payload.Notify, service.NewSystemTaskProgressReporter(task, runnerID))
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// modelUpdateHandler runs the scheduled upstream model update detection job.
type modelUpdateHandler struct{}

func (modelUpdateHandler) Type() string { return model.SystemTaskTypeModelUpdate }

func (modelUpdateHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", true)
}

func (modelUpdateHandler) Interval() time.Duration {
	intervalMinutes := common.GetEnvOrDefaultBounded(
		"CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES",
		channelUpstreamModelUpdateTaskDefaultIntervalMinutes,
		1,
		maxScheduledTaskIntervalMinutes,
	)
	return time.Duration(intervalMinutes) * time.Minute
}

func (modelUpdateHandler) NewPayload() any { return nil }

// modelUpdateTaskPayload controls one model_update run. A scheduled run
// (Manual=false) respects the per-channel minimum check interval and may
// auto-apply detected models when a channel has auto-sync enabled. A manual
// "detect all" trigger sets Manual=true to reproduce the legacy detect-all
// semantics: force a re-check regardless of the interval and never auto-apply,
// so the admin reviews and applies changes explicitly.
type modelUpdateTaskPayload struct {
	Manual bool `json:"manual,omitempty"`
}

func (modelUpdateHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := modelUpdateTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary := runChannelUpstreamModelUpdateTaskOnce(ctx, payload.Manual, !payload.Manual, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// midjourneyPollHandler runs one Midjourney polling pass per scheduled run.
// Enabled() folds the "are there unfinished tasks?" check into enablement so the
// scheduler creates no row when the system is idle; only when at least one
// Midjourney task is in progress does a row get scheduled.
type midjourneyPollHandler struct{}

func (midjourneyPollHandler) Type() string { return model.SystemTaskTypeMidjourneyPoll }

func (midjourneyPollHandler) Enabled() bool {
	// Billing recovery is independent from provider polling. A failed task can
	// already be terminal/100% when a process exits before clearing its quota,
	// so keep this handler schedulable until that durable marker is reconciled,
	// even when UPDATE_TASK is disabled.
	pendingRefund, err := model.HasMidjourneyTasksWithPendingRefundWithError()
	if err != nil {
		// Keep the worker schedulable while the database is unavailable. Returning
		// false would turn an outage into an apparently idle queue and strand
		// provider/billing tasks indefinitely.
		common.SysLog(fmt.Sprintf("midjourney refund probe failed: %v", err))
		return true
	}
	if pendingRefund {
		return true
	}
	pendingIntent, err := model.HasMidjourneySubmitIntentsNeedingReconcile()
	if err != nil {
		common.SysLog(fmt.Sprintf("midjourney submit intent probe failed: %v", err))
		return true
	}
	if pendingIntent {
		return true
	}
	if !constant.UpdateTask {
		return false
	}
	unfinished, err := model.HasUnfinishedMidjourneyTasksWithError()
	if err != nil {
		common.SysLog(fmt.Sprintf("midjourney task probe failed: %v", err))
		return true
	}
	return unfinished
}

func (midjourneyPollHandler) Interval() time.Duration { return 15 * time.Second }

func (midjourneyPollHandler) NewPayload() any { return nil }

func (midjourneyPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	// When provider polling is disabled, a pending terminal refund still needs
	// a worker. Run only the durable accounting pass in that mode; the normal
	// polling helper intentionally remains flag-agnostic for direct/manual
	// callers and existing tests.
	if !constant.UpdateTask {
		summary := midjourneyPollSummary{}
		var intentErr, refundErr error
		summary.SubmitIntentCandidates,
			summary.SubmitIntentCompleted,
			summary.SubmitIntentPending,
			intentErr = service.ReconcilePendingMidjourneySubmitIntentsWithError(ctx, model.TaskBillingReconcileBatchLimit)
		summary.PendingRefundCandidates,
			summary.PendingRefundCompleted,
			summary.PendingRefundPending,
			refundErr = service.ReconcilePendingMidjourneyRefundsWithError(ctx, model.TaskBillingReconcileBatchLimit)
		if intentErr != nil || refundErr != nil {
			summary.PollErrors++
		}
		if summary.PollErrors > 0 {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary, fmt.Errorf("Midjourney billing reconciliation encountered %d error(s)", summary.PollErrors))
		} else {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
		}
		return
	}
	summary := runMidjourneyTaskUpdateOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	if summary.PollErrors > 0 {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary,
			fmt.Errorf("Midjourney polling encountered %d error(s)", summary.PollErrors))
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// asyncTaskPollHandler runs one async-task (Suno/video) polling pass per
// scheduled run. Like midjourneyPollHandler, Enabled() folds in the unfinished
// task existence check so an idle system schedules no rows.
type asyncTaskPollHandler struct{}

func (asyncTaskPollHandler) Type() string { return model.SystemTaskTypeAsyncTaskPoll }

func (asyncTaskPollHandler) Enabled() bool {
	// A failed async task may have retained its durable quota marker after a
	// transient refund/funding error. Keep the polling worker schedulable until
	// that marker is reconciled, even when no provider task remains in flight.
	// Keep the scheduler probe in lock-step with both replay workers. A pending
	// compatibility alias (for example terminal_settle or wallet_refund) may be
	// the only remaining work after a task row has disappeared; hard-coding the
	// historical four names here would make that durable marker sleep forever.
	billingComponents := append(
		service.TaskBillingOperationComponents(),
		service.BillingRefundOperationComponents()...,
	)
	now := time.Now().Unix()
	billingPending, err := model.HasPendingBillingOperationsWithError(billingComponents)
	if err != nil {
		common.SysLog(fmt.Sprintf("billing operation probe failed: %v", err))
		return true
	}
	if !billingPending {
		billingPending, err = model.HasPendingBillingSettlementsWithError(now)
		if err != nil {
			common.SysLog(fmt.Sprintf("billing settlement probe failed: %v", err))
			return true
		}
	}
	if !billingPending {
		billingPending, err = model.HasPendingBillingAdjustmentsWithError(now)
		if err != nil {
			common.SysLog(fmt.Sprintf("billing adjustment probe failed: %v", err))
			return true
		}
	}
	if !billingPending {
		billingPending, err = model.HasTerminalTasksWithPendingQuotaWithError(model.TaskRefundLegacyCutoff, now)
		if err != nil {
			common.SysLog(fmt.Sprintf("terminal billing probe failed: %v", err))
			return true
		}
	}
	if billingPending {
		return true
	}
	// UPDATE_TASK controls provider polling only. Billing reconciliation must
	// remain schedulable while that feature flag is disabled, otherwise a
	// committed request can strand its durable usage/settlement markers.
	if !constant.UpdateTask {
		return false
	}
	unfinished, err := model.HasUnfinishedSyncTasksWithError()
	if err != nil {
		common.SysLog(fmt.Sprintf("async task probe failed: %v", err))
		return true
	}
	return unfinished
}

func (asyncTaskPollHandler) Interval() time.Duration { return 15 * time.Second }

func (asyncTaskPollHandler) NewPayload() any { return nil }

func (asyncTaskPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary, err := service.RunTaskPollingOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

func finishSystemTaskHandler(task *model.SystemTask, runnerID string, status model.SystemTaskStatus, result any, runErr error) {
	errorMessage := ""
	if runErr != nil {
		errorMessage = runErr.Error()
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, errorMessage); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}
