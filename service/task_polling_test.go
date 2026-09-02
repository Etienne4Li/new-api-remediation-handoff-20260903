package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type taskPollingFetchAdaptor struct {
	mu           sync.Mutex
	taskIDs      []string
	panicTaskIDs map[string]struct{}
	fetched      chan string
	blockTaskID  string
	blockStarted chan struct{}
	releaseBlock chan struct{}
	blockOnce    sync.Once
}

// taskPollingIdentityMismatchAdaptor models a provider that returns an
// explicit task identity for a different task than the one requested. The
// polling layer must reject that response before mutating status/data or
// invoking any billing side effect.
type taskPollingIdentityMismatchAdaptor struct {
	responseID string
}

type taskPollingResultAdaptor struct {
	result      relaycommon.TaskInfo
	beforeParse func() error
	statusCode  int
	body        io.ReadCloser
	finalQuota  int
}

type taskPollingTrackingBody struct {
	io.Reader
	closed bool
}

type taskPollingCountingReader struct {
	remaining int
	read      int
}

func (r *taskPollingCountingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}

type taskPollingConcurrencyTracker struct {
	mu      sync.Mutex
	active  int
	peak    int
	total   int
	started chan struct{}
	release chan struct{}
}

type taskPollingConcurrencyAdaptor struct {
	tracker *taskPollingConcurrencyTracker
}

func (b *taskPollingTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestTaskPollingDiagnosticBodyMetaBoundsAndRedactsBody(t *testing.T) {
	reader := &taskPollingCountingReader{remaining: int(maxTaskPollingDiagnosticBytes) + 4096}
	meta := taskPollingDiagnosticBodyMeta(reader)

	assert.Equal(t, int(maxTaskPollingDiagnosticBytes)+1, reader.read)
	assert.Contains(t, meta, "truncated=true")
	assert.NotContains(t, meta, strings.Repeat("x", 32))
}

func (a *taskPollingConcurrencyAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *taskPollingConcurrencyAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	return a.FetchTaskWithContext(context.Background(), baseURL, key, body, proxy)
}

func (a *taskPollingConcurrencyAdaptor) FetchTaskWithContext(ctx context.Context, _ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	tracker := a.tracker
	tracker.mu.Lock()
	tracker.active++
	tracker.total++
	if tracker.active > tracker.peak {
		tracker.peak = tracker.active
	}
	tracker.mu.Unlock()
	tracker.started <- struct{}{}
	defer func() {
		tracker.mu.Lock()
		tracker.active--
		tracker.mu.Unlock()
	}()

	select {
	case <-tracker.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	taskID, _ := body["task_id"].(string)
	responseBody, err := common.Marshal(taskdto.TaskResponse[model.Task]{
		Code: taskdto.TaskSuccessCode,
		Data: model.Task{TaskID: taskID, Status: model.TaskStatusInProgress, Progress: "30%"},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(responseBody))}, nil
}

func (a *taskPollingConcurrencyAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}, nil
}

func (a *taskPollingConcurrencyAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingResultAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *taskPollingResultAdaptor) FetchTask(_ string, _ string, _ map[string]any, _ string) (*http.Response, error) {
	statusCode := a.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	body := a.body
	if body == nil {
		body = io.NopCloser(strings.NewReader(`{}`))
	}
	return &http.Response{
		StatusCode: statusCode,
		Body:       body,
	}, nil
}

func (a *taskPollingResultAdaptor) FetchTaskWithContext(ctx context.Context, baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.FetchTask(baseURL, key, body, proxy)
}

func (a *taskPollingResultAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	if a.beforeParse != nil {
		if err := a.beforeParse(); err != nil {
			return nil, err
		}
	}
	result := a.result
	return &result, nil
}

func (a *taskPollingResultAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return a.finalQuota
}

func (a *taskPollingIdentityMismatchAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *taskPollingIdentityMismatchAdaptor) FetchTask(_ string, _ string, _ map[string]any, _ string) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}, nil
}

func (a *taskPollingIdentityMismatchAdaptor) FetchTaskWithContext(ctx context.Context, baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.FetchTask(baseURL, key, body, proxy)
}

func (a *taskPollingIdentityMismatchAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{
		TaskID: a.responseID,
		Status: model.TaskStatusSuccess,
	}, nil
}

func (a *taskPollingIdentityMismatchAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

type sunoFailurePollingAdaptor struct {
	failReason string
	submitTime int64
}

type sunoEmptyPollingAdaptor struct {
	mu      sync.Mutex
	fetched int
}

func (a *sunoEmptyPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *sunoEmptyPollingAdaptor) FetchTask(_ string, _ string, _ map[string]any, _ string) (*http.Response, error) {
	a.mu.Lock()
	a.fetched++
	a.mu.Unlock()
	body, err := common.Marshal(taskdto.TaskResponse[[]taskdto.SunoDataResponse]{
		Code: taskdto.TaskSuccessCode,
		Data: []taskdto.SunoDataResponse{},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (a *sunoEmptyPollingAdaptor) FetchTaskWithContext(ctx context.Context, baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.FetchTask(baseURL, key, body, proxy)
}

func (a *sunoEmptyPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (a *sunoEmptyPollingAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

func (a *sunoEmptyPollingAdaptor) fetchCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fetched
}

func (a *sunoFailurePollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *sunoFailurePollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskIDs, _ := body["ids"].([]string)
	items := make([]taskdto.SunoDataResponse, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		items = append(items, taskdto.SunoDataResponse{
			TaskID:     taskID,
			Status:     string(model.TaskStatusFailure),
			FailReason: a.failReason,
			SubmitTime: a.submitTime,
			FinishTime: time.Now().Unix(),
		})
	}

	responseBody, err := common.Marshal(taskdto.TaskResponse[[]taskdto.SunoDataResponse]{
		Code: taskdto.TaskSuccessCode,
		Data: items,
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a *sunoFailurePollingAdaptor) FetchTaskWithContext(ctx context.Context, baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.FetchTask(baseURL, key, body, proxy)
}

func (a *sunoFailurePollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (a *sunoFailurePollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingFetchAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *taskPollingFetchAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	return a.FetchTaskWithContext(context.Background(), "", "", body, "")
}

func (a *taskPollingFetchAdaptor) FetchTaskWithContext(ctx context.Context, _ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	if _, shouldPanic := a.panicTaskIDs[taskID]; shouldPanic {
		panic("provider polling panic")
	}
	if taskID == a.blockTaskID && a.releaseBlock != nil {
		a.blockOnce.Do(func() {
			if a.blockStarted != nil {
				close(a.blockStarted)
			}
		})
		select {
		case <-a.releaseBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.taskIDs = append(a.taskIDs, taskID)
	a.mu.Unlock()
	if a.fetched != nil {
		select {
		case a.fetched <- taskID:
		default:
		}
	}

	response := taskdto.TaskResponse[model.Task]{
		Code: taskdto.TaskSuccessCode,
		Data: model.Task{
			TaskID:   taskID,
			Status:   model.TaskStatusInProgress,
			Progress: "30%",
		},
	}
	responseBody, err := common.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a *taskPollingFetchAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}, nil
}

func (a *taskPollingFetchAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingFetchAdaptor) fetchCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.taskIDs)
}

func (a *taskPollingFetchAdaptor) fetchedTaskIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.taskIDs...)
}

func seedTaskPollingChannel(t *testing.T, id int, disableSleep bool) {
	t.Helper()
	ch := &model.Channel{
		Id:     id,
		Type:   constant.ChannelTypeKling,
		Name:   "polling_channel",
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}
	if disableSleep {
		ch.SetOtherSettings(dto.ChannelOtherSettings{DisableTaskPollingSleep: true})
	}
	require.NoError(t, model.DB.Create(ch).Error)
}

func seedPollingTask(t *testing.T, channelID int, publicID string, upstreamID string) *model.Task {
	t.Helper()
	task := &model.Task{
		TaskID:    publicID,
		Platform:  constant.TaskPlatform("kling"),
		UserId:    1,
		ChannelId: channelID,
		Action:    constant.TaskActionGenerate,
		Status:    model.TaskStatusInProgress,
		Progress:  "30%",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: upstreamID,
		},
	}
	require.NoError(t, model.DB.Create(task).Error)
	return task
}

func TestUpdateVideoTasksDefaultSleepWaitsBetweenTasks(t *testing.T) {
	truncate(t)

	const channelID = 101
	seedTaskPollingChannel(t, channelID, false)
	first := seedPollingTask(t, channelID, "task_public_1", "upstream_1")
	second := seedPollingTask(t, channelID, "task_public_2", "upstream_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		channelID: {
			first.GetUpstreamTaskID(),
			second.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		first.GetUpstreamTaskID():  first,
		second.GetUpstreamTaskID(): second,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, adaptor.fetchCount())
}

func TestUpdateVideoTasksCanSkipPollingSleepPerChannel(t *testing.T) {
	truncate(t)

	const channelID = 102
	seedTaskPollingChannel(t, channelID, true)
	first := seedPollingTask(t, channelID, "task_public_3", "upstream_3")
	second := seedPollingTask(t, channelID, "task_public_4", "upstream_4")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		channelID: {
			first.GetUpstreamTaskID(),
			second.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		first.GetUpstreamTaskID():  first,
		second.GetUpstreamTaskID(): second,
	})

	require.NoError(t, err)
	assert.Equal(t, 2, adaptor.fetchCount())
}

func TestUpdateVideoTasksDefaultSleepDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const firstChannelID = 201
	const secondChannelID = 202
	seedTaskPollingChannel(t, firstChannelID, false)
	seedTaskPollingChannel(t, secondChannelID, false)
	firstChannelFirst := seedPollingTask(t, firstChannelID, "task_public_5", "upstream_a_1")
	firstChannelSecond := seedPollingTask(t, firstChannelID, "task_public_6", "upstream_a_2")
	secondChannelFirst := seedPollingTask(t, secondChannelID, "task_public_7", "upstream_b_1")
	secondChannelSecond := seedPollingTask(t, secondChannelID, "task_public_8", "upstream_b_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		firstChannelID: {
			firstChannelFirst.GetUpstreamTaskID(),
			firstChannelSecond.GetUpstreamTaskID(),
		},
		secondChannelID: {
			secondChannelFirst.GetUpstreamTaskID(),
			secondChannelSecond.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		firstChannelFirst.GetUpstreamTaskID():   firstChannelFirst,
		firstChannelSecond.GetUpstreamTaskID():  firstChannelSecond,
		secondChannelFirst.GetUpstreamTaskID():  secondChannelFirst,
		secondChannelSecond.GetUpstreamTaskID(): secondChannelSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_a_1", "upstream_b_1"}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksSlowChannelDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const slowChannelID = 251
	const fastChannelID = 252
	seedTaskPollingChannel(t, slowChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	slowTask := seedPollingTask(t, slowChannelID, "task_public_slow", "upstream_slow_1")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_fast_1", "upstream_fast_parallel_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_fast_2", "upstream_fast_parallel_2")

	adaptor := &taskPollingFetchAdaptor{
		fetched:      make(chan string, 4),
		blockTaskID:  slowTask.GetUpstreamTaskID(),
		blockStarted: make(chan struct{}),
		releaseBlock: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseBlockedTask := func() {
		releaseOnce.Do(func() {
			close(adaptor.releaseBlock)
		})
	}
	t.Cleanup(releaseBlockedTask)
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	errCh := make(chan error, 1)
	gopool.Go(func() {
		errCh <- UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), map[int][]string{
			slowChannelID: {
				slowTask.GetUpstreamTaskID(),
			},
			fastChannelID: {
				fastFirst.GetUpstreamTaskID(),
				fastSecond.GetUpstreamTaskID(),
			},
		}, map[string]*model.Task{
			slowTask.GetUpstreamTaskID():   slowTask,
			fastFirst.GetUpstreamTaskID():  fastFirst,
			fastSecond.GetUpstreamTaskID(): fastSecond,
		})
	})

	select {
	case <-adaptor.blockStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slow channel did not start blocking")
	}

	require.Eventually(t, func() bool {
		fetchedTaskIDs := adaptor.fetchedTaskIDs()
		return len(fetchedTaskIDs) == 2 &&
			fetchedTaskIDs[0] == fastFirst.GetUpstreamTaskID() &&
			fetchedTaskIDs[1] == fastSecond.GetUpstreamTaskID()
	}, 500*time.Millisecond, 10*time.Millisecond)

	releaseBlockedTask()
	require.NoError(t, <-errCh)
	assert.ElementsMatch(t, []string{
		slowTask.GetUpstreamTaskID(),
		fastFirst.GetUpstreamTaskID(),
		fastSecond.GetUpstreamTaskID(),
	}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksMixedChannelSleepSettings(t *testing.T) {
	truncate(t)

	const sleepyChannelID = 301
	const fastChannelID = 302
	seedTaskPollingChannel(t, sleepyChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	sleepyFirst := seedPollingTask(t, sleepyChannelID, "task_public_9", "upstream_sleepy_1")
	sleepySecond := seedPollingTask(t, sleepyChannelID, "task_public_10", "upstream_sleepy_2")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_11", "upstream_fast_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_12", "upstream_fast_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), map[int][]string{
		sleepyChannelID: {
			sleepyFirst.GetUpstreamTaskID(),
			sleepySecond.GetUpstreamTaskID(),
		},
		fastChannelID: {
			fastFirst.GetUpstreamTaskID(),
			fastSecond.GetUpstreamTaskID(),
		},
	}, map[string]*model.Task{
		sleepyFirst.GetUpstreamTaskID():  sleepyFirst,
		sleepySecond.GetUpstreamTaskID(): sleepySecond,
		fastFirst.GetUpstreamTaskID():    fastFirst,
		fastSecond.GetUpstreamTaskID():   fastSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_sleepy_1", "upstream_fast_1", "upstream_fast_2"}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksBoundsChannelConcurrency(t *testing.T) {
	truncate(t)

	const expectedMaxConcurrency = 16
	const extraChannels = 1
	totalChannels := expectedMaxConcurrency + extraChannels
	tracker := &taskPollingConcurrencyTracker{
		started: make(chan struct{}, totalChannels),
		release: make(chan struct{}),
	}
	taskChannelM := make(map[int][]string, totalChannels)
	taskM := make(map[string]*model.Task, totalChannels)
	for i := 0; i < totalChannels; i++ {
		channelID := 10_000 + i
		seedTaskPollingChannel(t, channelID, true)
		upstreamID := fmt.Sprintf("bounded-channel-%d", i)
		task := seedPollingTask(t, channelID, "task_"+upstreamID, upstreamID)
		taskChannelM[channelID] = []string{upstreamID}
		taskM[pollingTaskKey(channelID, upstreamID)] = task
	}

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return &taskPollingConcurrencyAdaptor{tracker: tracker}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	done := make(chan error, 1)
	go func() {
		done <- UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), taskChannelM, taskM)
	}()

	for i := 0; i < expectedMaxConcurrency; i++ {
		select {
		case <-tracker.started:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded polling workers did not start")
		}
	}
	select {
	case <-tracker.started:
		t.Fatal("started more provider requests than the channel concurrency bound")
	case <-time.After(100 * time.Millisecond):
	}

	close(tracker.release)
	require.NoError(t, <-done)
	tracker.mu.Lock()
	peak, total := tracker.peak, tracker.total
	tracker.mu.Unlock()
	assert.LessOrEqual(t, peak, expectedMaxConcurrency)
	assert.Equal(t, totalChannels, total)
}

func TestUpdateVideoTasksReturnsChannelErrorsAndContinuesOtherChannels(t *testing.T) {
	truncate(t)

	const failedChannelID, healthyChannelID = 10_101, 10_102
	seedTaskPollingChannel(t, failedChannelID, true)
	seedTaskPollingChannel(t, healthyChannelID, true)
	healthy := seedPollingTask(t, healthyChannelID, "healthy-public", "healthy-upstream")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	err := UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), map[int][]string{
		failedChannelID:  {"missing-local-task"},
		healthyChannelID: {healthy.GetUpstreamTaskID()},
	}, map[string]*model.Task{
		pollingTaskKey(healthyChannelID, healthy.GetUpstreamTaskID()): healthy,
	})

	require.Error(t, err)
	assert.Equal(t, []string{healthy.GetUpstreamTaskID()}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksRecoversPerJobPanicAndDrainsQueue(t *testing.T) {
	truncate(t)

	const panickingChannels = maxTaskPollingChannelConcurrency
	taskChannelM := make(map[int][]string, panickingChannels+1)
	taskM := make(map[string]*model.Task, panickingChannels+1)
	panicTaskIDs := make(map[string]struct{}, panickingChannels)
	for i := 0; i <= panickingChannels; i++ {
		channelID := 11_000 + i
		seedTaskPollingChannel(t, channelID, true)
		upstreamID := fmt.Sprintf("panic-isolation-%d", i)
		task := seedPollingTask(t, channelID, "task_"+upstreamID, upstreamID)
		taskChannelM[channelID] = []string{upstreamID}
		taskM[pollingTaskKey(channelID, upstreamID)] = task
		if i < panickingChannels {
			panicTaskIDs[upstreamID] = struct{}{}
		}
	}

	adaptor := &taskPollingFetchAdaptor{panicTaskIDs: panicTaskIDs}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), taskChannelM, taskM)

	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("%d video channel(s) failed to poll", panickingChannels))
	assert.Equal(t, []string{fmt.Sprintf("panic-isolation-%d", panickingChannels)}, adaptor.fetchedTaskIDs())
}

func TestUpdateSunoTasksReturnsChannelErrorsAndContinuesOtherChannels(t *testing.T) {
	truncate(t)

	const missingChannelID, healthyChannelID = 10_201, 10_202
	seedTaskPollingChannel(t, healthyChannelID, true)
	adaptor := &sunoEmptyPollingAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	err := UpdateSunoTasks(context.Background(), map[int][]string{
		missingChannelID: {"missing-channel-task"},
		healthyChannelID: {"healthy-channel-task"},
	}, nil)

	require.Error(t, err)
	assert.Equal(t, 1, adaptor.fetchCount(), "the healthy channel must still be polled after another channel fails")
}

func TestUpdateVideoTasksScopesDuplicateUpstreamIDsByChannel(t *testing.T) {
	truncate(t)

	const firstChannelID, secondChannelID = 305, 306
	seedTaskPollingChannel(t, firstChannelID, true)
	seedTaskPollingChannel(t, secondChannelID, true)
	first := seedPollingTask(t, firstChannelID, "task_public_same_1", "upstream_same")
	second := seedPollingTask(t, secondChannelID, "task_public_same_2", "upstream_same")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	// Provider IDs are only unique within a channel. The polling map therefore
	// carries both channel-scoped entries even though the upstream IDs match.
	err := UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), map[int][]string{
		firstChannelID:  {first.GetUpstreamTaskID()},
		secondChannelID: {second.GetUpstreamTaskID()},
	}, map[string]*model.Task{
		pollingTaskKey(firstChannelID, first.GetUpstreamTaskID()):   first,
		pollingTaskKey(secondChannelID, second.GetUpstreamTaskID()): second,
	})
	require.NoError(t, err)

	var reloadedFirst, reloadedSecond model.Task
	require.NoError(t, model.DB.First(&reloadedFirst, first.ID).Error)
	require.NoError(t, model.DB.First(&reloadedSecond, second.ID).Error)
	assert.NotEmpty(t, reloadedFirst.Data)
	assert.NotEmpty(t, reloadedSecond.Data)
	assert.ElementsMatch(t, []string{"upstream_same", "upstream_same"}, adaptor.fetchedTaskIDs())
}

func TestLookupPollingTaskRejectsCrossChannelLegacyEntry(t *testing.T) {
	task := &model.Task{ChannelId: 401, TaskID: "upstream"}
	tasks := map[string]*model.Task{"upstream": task}
	assert.Nil(t, lookupPollingTask(tasks, 402, "upstream"))
	assert.Same(t, task, lookupPollingTask(tasks, 401, "upstream"))
}

func TestLookupPollingTaskRejectsUnknownChannelWildcardEntry(t *testing.T) {
	// A zero channel is an unknown namespace, not a wildcard. Accepting it for
	// every requested channel would let a legacy/bad map entry receive another
	// channel's provider response and potentially trigger its billing path.
	task := &model.Task{ChannelId: 0, TaskID: "upstream"}
	tasks := map[string]*model.Task{"upstream": task}
	assert.Nil(t, lookupPollingTask(tasks, 401, "upstream"))
	assert.Nil(t, lookupPollingTask(tasks, 0, "upstream"), "an unknown requested channel must fail closed too")
}

func TestUpdateVideoSingleTaskRejectsMismatchedProviderResponseID(t *testing.T) {
	truncate(t)

	const channelID = 307
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "task_public_identity_guard", "provider-identity-expected")
	task.Data = []byte(`{"status":"before"}`)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("data", task.Data).Error)

	adaptor := &taskPollingIdentityMismatchAdaptor{responseID: "provider-identity-other"}
	err := updateVideoSingleTask(context.Background(), adaptor, &model.Channel{
		Id:     channelID,
		Type:   constant.ChannelTypeKling,
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}, task.GetUpstreamTaskID(), map[string]*model.Task{
		pollingTaskKey(channelID, task.GetUpstreamTaskID()): task,
	})
	require.ErrorIs(t, err, ErrTaskPollingIdentityMismatch)

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, reloaded.Status)
	assert.Equal(t, "30%", reloaded.Progress)
	assert.Equal(t, []byte(`{"status":"before"}`), []byte(reloaded.Data))
	assert.Equal(t, "provider-identity-expected", reloaded.GetUpstreamTaskID())
}

func TestUpdateVideoSingleTaskPersistsRemoteURLPrivately(t *testing.T) {
	truncate(t)

	const channelID = 308
	const signedURL = "https://storage.example/video.mp4?X-Goog-Signature=private"
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "task_public_remote_url", "provider-remote-url")
	adaptor := &taskPollingResultAdaptor{result: relaycommon.TaskInfo{
		TaskID:    task.GetUpstreamTaskID(),
		Status:    model.TaskStatusSuccess,
		RemoteUrl: signedURL,
	}}

	err := updateVideoSingleTask(context.Background(), adaptor, &model.Channel{
		Id:     channelID,
		Type:   constant.ChannelTypeKling,
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}, task.GetUpstreamTaskID(), map[string]*model.Task{
		pollingTaskKey(channelID, task.GetUpstreamTaskID()): task,
	})
	require.NoError(t, err)

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, signedURL, reloaded.PrivateData.ResultURL)
	assert.Equal(t, signedURL, task.PrivateData.ResultURL)
}

func TestUpdateVideoSingleTaskCASLossReloadsPersistedResultURL(t *testing.T) {
	truncate(t)

	const channelID = 309
	const persistedURL = "https://storage.example/winner.mp4?signature=persisted"
	const staleURL = "https://storage.example/loser.mp4?signature=stale"
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "task_public_cas_reload", "provider-cas-reload")

	adaptor := &taskPollingResultAdaptor{result: relaycommon.TaskInfo{
		TaskID:    task.GetUpstreamTaskID(),
		Status:    model.TaskStatusSuccess,
		RemoteUrl: staleURL,
	}}
	adaptor.beforeParse = func() error {
		winner := *task
		winner.Status = model.TaskStatusSuccess
		winner.Progress = taskcommon.ProgressComplete
		winner.PrivateData.ResultURL = persistedURL
		won, err := winner.UpdateWithStatus(model.TaskStatusInProgress)
		if err != nil {
			return err
		}
		if !won {
			return fmt.Errorf("test winner failed to acquire task CAS")
		}
		return nil
	}

	err := updateVideoSingleTask(context.Background(), adaptor, &model.Channel{
		Id:     channelID,
		Type:   constant.ChannelTypeKling,
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}, task.GetUpstreamTaskID(), map[string]*model.Task{
		pollingTaskKey(channelID, task.GetUpstreamTaskID()): task,
	})
	require.NoError(t, err)

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, persistedURL, reloaded.PrivateData.ResultURL)
	assert.Equal(t, persistedURL, task.PrivateData.ResultURL)
	assert.NotEqual(t, staleURL, task.PrivateData.ResultURL)
}

func TestUpdateVideoSingleTaskDoesNotTreatHTTPErrorAsTerminalFailure(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 312, 312, 312
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 900
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-video-http-error", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)
	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "video-http-error-not-terminal"
	task.PrivateData.UpstreamTaskID = "provider-video-http-error"
	task.Status = model.TaskStatusInProgress
	task.Progress = "30%"
	task.SubmitTime = time.Now().Unix()
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &taskPollingResultAdaptor{
		statusCode: http.StatusUnauthorized,
		result: relaycommon.TaskInfo{
			TaskID: task.GetUpstreamTaskID(),
			Status: model.TaskStatusFailure,
			Reason: "key rejected while provider task is still running",
		},
	}
	err := updateVideoSingleTask(context.Background(), adaptor, &model.Channel{
		Id:     channelID,
		Type:   constant.ChannelTypeKling,
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}, task.GetUpstreamTaskID(), map[string]*model.Task{
		pollingTaskKey(channelID, task.GetUpstreamTaskID()): task,
	})
	require.Error(t, err)

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, reloaded.Status)
	assert.Equal(t, taskQuota, reloaded.Quota)
	assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-taskQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, taskQuota, getTokenUsedQuota(t, tokenID))
	assert.Zero(t, countLogs(t))
}

func TestUpdateVideoSingleTaskDoesNotPersistAfterPollingContextCancellation(t *testing.T) {
	truncate(t)

	const channelID = 313
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "task_public_cancelled_poll", "provider-cancelled-poll")
	ctx, cancel := context.WithCancel(context.Background())
	adaptor := &taskPollingResultAdaptor{
		result: relaycommon.TaskInfo{
			TaskID: task.GetUpstreamTaskID(),
			Status: model.TaskStatusSuccess,
		},
		beforeParse: func() error {
			cancel()
			return nil
		},
	}

	err := updateVideoSingleTask(ctx, adaptor, &model.Channel{
		Id:     channelID,
		Type:   constant.ChannelTypeKling,
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}, task.GetUpstreamTaskID(), map[string]*model.Task{
		pollingTaskKey(channelID, task.GetUpstreamTaskID()): task,
	})
	require.ErrorIs(t, err, context.Canceled)

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusInProgress, reloaded.Status)
	assert.Equal(t, "30%", reloaded.Progress)
}

func TestUpdateVideoSingleTaskPersistsSuccessWhenBillingAdjustmentIsInvalid(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 314, 314, 314
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 900
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-video-invalid-adjustment", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "video-success-invalid-adjustment"
	task.PrivateData.UpstreamTaskID = "provider-video-success-invalid-adjustment"
	task.Status = model.TaskStatusInProgress
	task.Progress = "30%"
	task.SubmitTime = time.Now().Add(-2 * time.Hour).Unix()
	task.BillingRequestId = "video-success-invalid-adjustment-request"
	task.BillingPreConsumedQuota = taskQuota
	task.BillingSettlementQuota = taskQuota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	task.BillingUsageRecorded = true
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &taskPollingResultAdaptor{
		result: relaycommon.TaskInfo{
			TaskID: task.GetUpstreamTaskID(),
			Status: model.TaskStatusSuccess,
		},
		finalQuota: common.MaxQuota + 1,
	}
	err := updateVideoSingleTask(context.Background(), adaptor, &model.Channel{
		Id:     channelID,
		Type:   constant.ChannelTypeKling,
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}, task.GetUpstreamTaskID(), map[string]*model.Task{
		pollingTaskKey(channelID, task.GetUpstreamTaskID()): task,
	})
	require.Error(t, err)

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, taskcommon.ProgressComplete, reloaded.Progress)
	assert.Equal(t, model.TaskBillingAdjustmentManual, reloaded.BillingAdjustmentState)
	assert.NotEmpty(t, reloaded.BillingAdjustmentError)
	assert.Equal(t, taskQuota, reloaded.Quota)
	assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-taskQuota, getTokenRemainQuota(t, tokenID))

	previousTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 60
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })
	sweepTimedOutTasks(context.Background())
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, taskQuota, reloaded.Quota)
}

func TestUpdateSunoTasksStalePollsRefundExactlyOnce(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 401, 401, 401
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 2_500
	const publicTaskID, upstreamTaskID = "suno_public_refund_once", "suno_upstream_refund_once"

	// Model the charge made when the task was submitted. The durable legacy
	// refund path now protects against restoring more token usage than the
	// token ledger actually records, so the fixture must carry that baseline.
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-suno-refund-once", initialTokenQuota-taskQuota)
	baseURL := "https://suno.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Type:    constant.ChannelTypeSunoAPI,
		Name:    "suno_refund_once",
		Key:     "sk-suno-channel",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
	}).Error)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = publicTaskID
	task.Platform = constant.TaskPlatformSuno
	task.Status = model.TaskStatusInProgress
	task.Progress = "50%"
	task.SubmitTime = time.Now().Unix()
	task.PrivateData.UpstreamTaskID = upstreamTaskID
	require.NoError(t, model.DB.Create(task).Error)

	var firstPollTask model.Task
	var staleSecondPollTask model.Task
	require.NoError(t, model.DB.First(&firstPollTask, task.ID).Error)
	require.NoError(t, model.DB.First(&staleSecondPollTask, task.ID).Error)

	adaptor := &sunoFailurePollingAdaptor{failReason: "upstream failed"}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		upstreamTaskID: &firstPollTask,
	}))
	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		upstreamTaskID: &staleSecondPollTask,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countLogs(t))
}

// A provider response can arrive out of order when the realtime fetch path
// races the background poller.  Once a Suno task is SUCCESS, a stale FAILURE
// response must not regress the terminal state or refund the already-settled
// charge.  (The video polling path already enforces this fence; Suno must do
// the same.)
func TestUpdateSunoTasksIgnoresStaleFailureAfterSuccess(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 411, 411, 411
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 2_500
	const publicTaskID, upstreamTaskID = "suno_public_success_then_stale_failure", "suno_upstream_success_then_stale_failure"

	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-suno-stale-success", initialTokenQuota-taskQuota)
	baseURL := "https://suno-stale.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Type:    constant.ChannelTypeSunoAPI,
		Name:    "suno_stale_success",
		Key:     "sk-suno-channel",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
	}).Error)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = publicTaskID
	task.Platform = constant.TaskPlatformSuno
	task.Status = model.TaskStatusSuccess
	task.Progress = taskcommon.ProgressComplete
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	task.PrivateData.UpstreamTaskID = upstreamTaskID
	require.NoError(t, model.DB.Create(task).Error)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return &sunoFailurePollingAdaptor{failReason: "stale provider failure"}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	var staleCopy model.Task
	require.NoError(t, model.DB.First(&staleCopy, task.ID).Error)
	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		upstreamTaskID: &staleCopy,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, taskcommon.ProgressComplete, reloaded.Progress)
	assert.Equal(t, taskQuota, reloaded.Quota)
	assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-taskQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(taskQuota), getChannelUsedQuota(t, channelID))
	assert.Zero(t, countLogs(t), "stale failure must not create a refund log")
}

func TestUpdateSunoTasksDoesNotReplaceLocalSubmitTime(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 412, 412, 412
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 750
	const upstreamTaskID = "suno-provider-submit-time"
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-suno-submit-time", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "suno-local-submit-time"
	task.Platform = constant.TaskPlatformSuno
	task.Status = model.TaskStatusInProgress
	task.Progress = "50%"
	task.SubmitTime = time.Now().Unix()
	localSubmitTime := task.SubmitTime
	task.PrivateData.UpstreamTaskID = upstreamTaskID
	task.BillingRequestId = "suno-local-submit-time-request"
	task.BillingPreConsumedQuota = taskQuota
	task.BillingSettlementQuota = taskQuota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	task.BillingUsageRecorded = true
	require.NoError(t, model.DB.Create(task).Error)

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return &sunoFailurePollingAdaptor{
			failReason: "provider failure",
			submitTime: model.TaskRefundLegacyCutoff - 1,
		}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{upstreamTaskID}, map[string]*model.Task{
		pollingTaskKey(channelID, upstreamTaskID): task,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, localSubmitTime, reloaded.SubmitTime)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
}

func TestUpdateSunoTasksClosesNonSuccessResponseBody(t *testing.T) {
	truncate(t)
	const channelID = 413
	seedChannel(t, channelID)
	body := &taskPollingTrackingBody{Reader: strings.NewReader(`{"error":"busy"}`)}
	adaptor := &taskPollingResultAdaptor{statusCode: http.StatusTooManyRequests, body: body}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	err := updateSunoTasks(context.Background(), channelID, []string{"upstream-busy"}, nil)
	require.Error(t, err)
	assert.True(t, body.closed)
}

func TestRunTaskPollingOnceDoesNotRefundHistoricalFailedTask(t *testing.T) {
	truncate(t)

	const userID, initialQuota, taskQuota = 402, 10_000, 1_200
	seedUser(t, userID, initialQuota)

	task := makeTask(userID, 0, taskQuota, 0, BillingSourceWallet, 0)
	task.TaskID = "historical_failed_already_refunded"
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	// Use the explicit rollout boundary rather than a relative duration. The
	// cutoff is intentionally fixed, so "90 days ago" becomes a modern task
	// once the test clock moves past the cutoff.
	task.SubmitTime = model.TaskRefundLegacyCutoff - 1
	task.UpdatedAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(task).Error)

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return &taskPollingFetchAdaptor{}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	summary, err := RunTaskPollingOnce(context.Background(), nil)
	require.NoError(t, err)

	assert.Zero(t, summary.UnfinishedTasks)
	assert.Equal(t, initialQuota, getUserQuota(t, userID))
	assert.Equal(t, taskQuota, getTaskQuota(t, task.ID))
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRunTaskPollingOnceReturnsProviderPollingErrors(t *testing.T) {
	truncate(t)

	const channelID = 10_301
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "provider-error-public", "provider-error-upstream")
	task.SubmitTime = time.Now().Unix()
	require.NoError(t, model.DB.Model(task).Update("submit_time", task.SubmitTime).Error)
	previousLimit := constant.TaskQueryLimit
	constant.TaskQueryLimit = 100
	t.Cleanup(func() { constant.TaskQueryLimit = previousLimit })

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return &taskPollingResultAdaptor{statusCode: http.StatusTooManyRequests}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	summary, err := RunTaskPollingOnce(context.Background(), nil)

	require.Error(t, err)
	assert.Equal(t, 1, summary.UnfinishedTasks)
	assert.Equal(t, 1, summary.FailedStages)
}

func TestSweepTimedOutTasksHonorsRefundRolloutBoundary(t *testing.T) {
	truncate(t)

	const (
		userID          = 403
		tokenID         = 403
		channelID       = 403
		initialQuota    = 10_000
		initialToken    = 6_000
		legacyTaskQuota = 1_800
		modernTaskQuota = 1_200
	)
	// The post-cutoff task represents a real wallet/token charge. Keeping the
	// charged baseline in the fixture lets the durable refund enforce its
	// token-used guard instead of manufacturing credit from an uncharged row.
	seedUser(t, userID, initialQuota-modernTaskQuota)
	seedToken(t, tokenID, userID, "sk-timeout-rollout", initialToken-modernTaskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, modernTaskQuota, 1)

	legacyTask := makeTask(userID, 0, legacyTaskQuota, 0, BillingSourceWallet, 0)
	legacyTask.TaskID = "legacy_timeout_without_refund"
	legacyTask.Progress = "50%"
	legacyTask.SubmitTime = 1771718399 // 2026-02-21 23:59:59 UTC
	require.NoError(t, model.DB.Create(legacyTask).Error)

	modernTask := makeTask(userID, channelID, modernTaskQuota, tokenID, BillingSourceWallet, 0)
	modernTask.TaskID = "modern_timeout_with_refund"
	modernTask.Progress = "50%"
	modernTask.SubmitTime = 1771718400 // 2026-02-22 00:00:00 UTC
	require.NoError(t, model.DB.Create(modernTask).Error)

	previousTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 1
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })

	sweepTimedOutTasks(context.Background())

	var reloadedLegacy model.Task
	var reloadedModern model.Task
	require.NoError(t, model.DB.First(&reloadedLegacy, legacyTask.ID).Error)
	require.NoError(t, model.DB.First(&reloadedModern, modernTask.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloadedLegacy.Status)
	assert.EqualValues(t, model.TaskStatusFailure, reloadedModern.Status)
	assert.Zero(t, reloadedLegacy.Quota)
	assert.Zero(t, reloadedModern.Quota)
	assert.Contains(t, reloadedLegacy.FailReason, "旧系统遗留任务")
	assert.Contains(t, reloadedModern.FailReason, "任务超时")
	assert.Equal(t, initialQuota, getUserQuota(t, userID))
	assert.Equal(t, initialToken, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestTimeoutFailureRacesProviderSuccessWithSingleBillingOutcome(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 414, 414, 414
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 1_450
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "timeout-success-race-token", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "timeout-success-race"
	task.Status = model.TaskStatusInProgress
	task.Progress = "90%"
	task.SubmitTime = time.Now().Add(-2 * time.Hour).Unix()
	task.BillingRequestId = "timeout-success-race-request"
	task.BillingPreConsumedQuota = taskQuota
	task.BillingSettlementQuota = taskQuota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	task.BillingUsageRecorded = true
	require.NoError(t, model.DB.Create(task).Error)

	var successCopy, timeoutCopy model.Task
	require.NoError(t, model.DB.First(&successCopy, task.ID).Error)
	require.NoError(t, model.DB.First(&timeoutCopy, task.ID).Error)
	start := make(chan struct{})
	type transitionResult struct {
		kind string
		won  bool
		err  error
	}
	results := make(chan transitionResult, 2)
	go func() {
		<-start
		previous := successCopy.Status
		successCopy.Status = model.TaskStatusSuccess
		successCopy.Progress = taskcommon.ProgressComplete
		successCopy.FinishTime = time.Now().Unix()
		won, err := successCopy.UpdateWithStatus(previous)
		results <- transitionResult{kind: "success", won: won, err: err}
	}()
	go func() {
		<-start
		won := failTaskAndRefund(context.Background(), &timeoutCopy, "timeout raced provider success")
		results <- transitionResult{kind: "timeout", won: won}
	}()
	close(start)

	winners := make(map[string]bool, 2)
	for i := 0; i < 2; i++ {
		result := <-results
		require.NoError(t, result.err)
		winners[result.kind] = result.won
	}
	assert.NotEqual(t, winners["success"], winners["timeout"], "exactly one terminal transition must own billing")

	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, task.ID).Error)
	if winners["success"] {
		assert.EqualValues(t, model.TaskStatusSuccess, persisted.Status)
		assert.Equal(t, taskQuota, persisted.Quota)
		assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID))
		assert.Equal(t, initialTokenQuota-taskQuota, getTokenRemainQuota(t, tokenID))
		assert.Equal(t, int64(0), countLogs(t))
	} else {
		assert.EqualValues(t, model.TaskStatusFailure, persisted.Status)
		assert.Zero(t, persisted.Quota)
		assert.Equal(t, model.TaskBillingReconcileComplete, persisted.BillingReconcileState)
		assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
		assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
		assert.Equal(t, int64(1), countLogs(t))
	}
}

func TestModernTaskConcurrentRefundsApplyOneLedgerOperation(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 415, 415, 415
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 1_350
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "modern-concurrent-refund-token", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "modern-concurrent-refund"
	task.Status = model.TaskStatusFailure
	task.Progress = taskcommon.ProgressComplete
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	task.BillingRequestId = "modern-concurrent-refund-request"
	task.BillingPreConsumedQuota = taskQuota
	task.BillingSettlementQuota = taskQuota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	task.BillingUsageRecorded = true
	task.BillingReconcileState = model.TaskBillingReconcilePending
	require.NoError(t, model.DB.Create(task).Error)

	const workers = 8
	copies := make([]model.Task, workers)
	for i := range copies {
		require.NoError(t, model.DB.First(&copies[i], task.ID).Error)
	}
	start := make(chan struct{})
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := range copies {
		wg.Add(1)
		go func(candidate *model.Task) {
			defer wg.Done()
			<-start
			results <- RefundTaskQuota(context.Background(), candidate, "concurrent terminal refund")
		}(&copies[i])
	}
	close(start)
	wg.Wait()
	close(results)
	completed := 0
	for result := range results {
		if result {
			completed++
		}
	}
	assert.GreaterOrEqual(t, completed, 1)

	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, task.ID).Error)
	assert.Zero(t, persisted.Quota)
	assert.Equal(t, model.TaskBillingReconcileComplete, persisted.BillingReconcileState)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countLogs(t))
	var operationCount int64
	require.NoError(t, model.DB.Model(&model.BillingOperation{}).
		Where("request_id = ? AND component = ?", task.BillingRequestId, model.BillingOperationTaskRefundComponent).
		Count(&operationCount).Error)
	assert.EqualValues(t, 1, operationCount)
}

func TestFailTaskAndRefundRestoresAccountingAndUsesCAS(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 404, 404, 404
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 2_500
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-polling-failure", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "polling-channel-failure"
	task.SubmitTime = time.Now().Unix()
	require.NoError(t, model.DB.Create(task).Error)
	// Model the charge made at task submission: the refund path must restore
	// both balances and the usage counters.
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	first, found, err := model.GetByTaskId(userID, task.TaskID)
	require.NoError(t, err)
	require.True(t, found)
	second, found, err := model.GetByTaskId(userID, task.TaskID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, first)
	require.NotNil(t, second)

	require.True(t, failTaskAndRefund(ctx, first, "channel missing"))
	// A stale concurrent poller must lose the status CAS and must not refund a
	// second time.
	assert.False(t, failTaskAndRefund(ctx, second, "channel missing"))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, string(model.TaskStatusFailure), string(reloaded.Status))
	assert.Equal(t, taskcommon.ProgressComplete, reloaded.Progress)
	assert.Zero(t, reloaded.Quota)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestRunTaskPollingOnceRefundsTaskWithMissingUpstreamID(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 405, 405, 405
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 1_500
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-polling-null-upstream", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	// Both the upstream id and historical fallback task_id are absent, which
	// is the malformed row handled by RunTaskPollingOnce's null-id branch.
	task.TaskID = ""
	task.Platform = constant.TaskPlatformSuno
	task.SubmitTime = time.Now().Unix()
	task.PrivateData.UpstreamTaskID = ""
	require.NoError(t, model.DB.Create(task).Error)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return &taskPollingFetchAdaptor{}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })
	previousLimit := constant.TaskQueryLimit
	constant.TaskQueryLimit = 100
	t.Cleanup(func() { constant.TaskQueryLimit = previousLimit })

	summary, err := RunTaskPollingOnce(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.NullTasksFailed)
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestTaskPollingMissingChannelRefundsSunoAndVideoTasks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform constant.TaskPlatform
		update   func(context.Context, int, []string, map[string]*model.Task) error
	}{
		{
			name:     "suno",
			platform: constant.TaskPlatformSuno,
			update: func(ctx context.Context, channelID int, ids []string, tasks map[string]*model.Task) error {
				return updateSunoTasks(ctx, channelID, ids, tasks)
			},
		},
		{
			name:     "video",
			platform: constant.TaskPlatform("kling"),
			update: func(ctx context.Context, channelID int, ids []string, tasks map[string]*model.Task) error {
				return updateVideoTasks(ctx, constant.TaskPlatform("kling"), channelID, ids, tasks)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			const userID, tokenID, channelID = 406, 406, 4_060
			const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 1_700
			seedUser(t, userID, initialUserQuota-taskQuota)
			seedToken(t, tokenID, userID, "sk-polling-missing-channel-"+tc.name, initialTokenQuota-taskQuota)
			task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
			task.TaskID = "polling-missing-channel-" + tc.name
			task.Platform = tc.platform
			task.PrivateData.UpstreamTaskID = "upstream-missing-channel-" + tc.name
			task.SubmitTime = time.Now().Unix()
			require.NoError(t, model.DB.Create(task).Error)
			seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

			err := tc.update(context.Background(), channelID, []string{task.GetUpstreamTaskID()}, map[string]*model.Task{
				task.GetUpstreamTaskID(): task,
			})
			require.Error(t, err)
			var reloaded model.Task
			require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
			assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
			assert.Zero(t, reloaded.Quota)
			assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
			assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
			assert.Equal(t, int64(1), countLogs(t))
		})
	}
}

func TestTaskPollingChannelLookupFailureDoesNotFailOrRefundTasks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform constant.TaskPlatform
		update   func(context.Context, int, []string, map[string]*model.Task) error
	}{
		{
			name:     "suno",
			platform: constant.TaskPlatformSuno,
			update: func(ctx context.Context, channelID int, ids []string, tasks map[string]*model.Task) error {
				return updateSunoTasks(ctx, channelID, ids, tasks)
			},
		},
		{
			name:     "video",
			platform: constant.TaskPlatform("kling"),
			update: func(ctx context.Context, channelID int, ids []string, tasks map[string]*model.Task) error {
				return updateVideoTasks(ctx, constant.TaskPlatform("kling"), channelID, ids, tasks)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			const userID, tokenID, channelID = 411, 411, 411
			const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 700
			seedUser(t, userID, initialUserQuota-taskQuota)
			seedToken(t, tokenID, userID, "sk-channel-lookup-outage-"+tc.name, initialTokenQuota-taskQuota)
			seedChannel(t, channelID)
			seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)
			task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
			task.TaskID = "channel-lookup-outage-" + tc.name
			task.PrivateData.UpstreamTaskID = "upstream-channel-lookup-outage-" + tc.name
			task.Platform = tc.platform
			task.SubmitTime = time.Now().Unix()
			require.NoError(t, model.DB.Create(task).Error)

			originalDB := model.DB
			brokenDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := brokenDB.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())
			originalMemoryCache := common.MemoryCacheEnabled
			common.MemoryCacheEnabled = false
			model.DB = brokenDB
			err = tc.update(context.Background(), channelID, []string{task.GetUpstreamTaskID()}, map[string]*model.Task{
				task.GetUpstreamTaskID(): task,
			})
			model.DB = originalDB
			common.MemoryCacheEnabled = originalMemoryCache
			t.Cleanup(func() {
				model.DB = originalDB
				common.MemoryCacheEnabled = originalMemoryCache
			})

			require.Error(t, err)
			assert.EqualValues(t, model.TaskStatusInProgress, task.Status, "a transient lookup error is not an authoritative task failure")
			var persisted model.Task
			require.NoError(t, model.DB.First(&persisted, task.ID).Error)
			assert.EqualValues(t, model.TaskStatusInProgress, persisted.Status)
			assert.Equal(t, taskQuota, persisted.Quota)
			assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID))
			assert.Equal(t, initialTokenQuota-taskQuota, getTokenRemainQuota(t, tokenID))
			assert.Zero(t, countLogs(t))
		})
	}
}

func TestReconcileTerminalTaskBillingRefundsOnceAndRetriesAfterLease(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 407, 407, 407
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 1_600
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-terminal-reconcile", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "terminal-reconcile-once"
	task.Status = model.TaskStatusFailure
	task.Progress = taskcommon.ProgressComplete
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	require.NoError(t, model.DB.Create(task).Error)

	candidates, refunded, pending := reconcileTerminalTaskBilling(ctx, 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, refunded)
	assert.Zero(t, pending)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTaskQuota(t, task.ID))
	assert.Equal(t, int64(1), countLogs(t))

	// Once the durable marker is cleared, a later pass has no candidate and
	// cannot issue a second funding/token refund.
	candidates, refunded, pending = reconcileTerminalTaskBilling(ctx, 100)
	assert.Zero(t, candidates)
	assert.Zero(t, refunded)
	assert.Zero(t, pending)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestReconcileTerminalTaskBillingHonorsLeaseAfterFailure(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 408, 408, 408
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 1_300
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-terminal-reconcile-failure", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "terminal-reconcile-retry"
	task.Status = model.TaskStatusFailure
	task.Progress = taskcommon.ProgressComplete
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_terminal_reconcile_token
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 408
		BEGIN
			SELECT RAISE(ABORT, 'forced terminal reconcile token failure');
		END;
	`).Error)

	candidates, refunded, pending := reconcileTerminalTaskBilling(ctx, 100)
	assert.Equal(t, 1, candidates)
	assert.Zero(t, refunded)
	assert.Equal(t, 1, pending)
	assert.Equal(t, taskQuota, getTaskQuota(t, task.ID))
	assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-taskQuota, getTokenRemainQuota(t, tokenID))

	// The lease prevents an immediate second attempt while the first worker may
	// still be unwinding a partial compensation. Because the token write failed
	// with an unknown commit outcome, the strict state machine fences the row as
	// manual instead of replaying a potentially duplicated refund after lease
	// expiry.
	candidates, refunded, pending = reconcileTerminalTaskBilling(ctx, 100)
	assert.Zero(t, candidates)
	assert.Zero(t, refunded)
	assert.Zero(t, pending)

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_terminal_reconcile_token").Error)
	var fenced model.Task
	require.NoError(t, model.DB.First(&fenced, task.ID).Error)
	assert.Equal(t, model.TaskBillingReconcileManual, fenced.BillingReconcileState)
	// An operator (or a reconciliation command after checking the provider
	// ledger) must explicitly requeue an uncertain attempt. Clearing only the
	// lease is insufficient and must not make it auto-replayable.
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"billing_reconcile_state": model.TaskBillingReconcilePending,
		"billing_reconcile_until": 0,
	}).Error)
	candidates, refunded, pending = reconcileTerminalTaskBilling(ctx, 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, refunded)
	assert.Zero(t, pending)
	assert.Zero(t, getTaskQuota(t, task.ID))
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
}

func TestReconcileTerminalTaskBillingConcurrentCAS(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 409, 409, 409
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 1_100
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "sk-terminal-reconcile-cas", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, taskQuota, 1)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "terminal-reconcile-cas"
	task.Status = model.TaskStatusFailure
	task.Progress = taskcommon.ProgressComplete
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	require.NoError(t, model.DB.Create(task).Error)

	results := make(chan [3]int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidates, refunded, pending := reconcileTerminalTaskBilling(ctx, 100)
			results <- [3]int{candidates, refunded, pending}
		}()
	}
	wg.Wait()
	close(results)

	var totalRefunded, totalPending int
	for result := range results {
		totalRefunded += result[1]
		totalPending += result[2]
	}
	assert.Equal(t, 1, totalRefunded, "CAS must allow only one terminal refund")
	assert.LessOrEqual(t, totalPending, 1)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTaskQuota(t, task.ID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestLegacyTerminalPollClearsMarkerWithoutRefund(t *testing.T) {
	truncate(t)
	const userID, channelID = 410, 410
	const initialUserQuota, taskQuota = 10_000, 1_200
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, 0, taskQuota, 1)

	baseURL := "https://suno-legacy.invalid"
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Updates(map[string]any{
		"type":     constant.ChannelTypeSunoAPI,
		"base_url": baseURL,
	}).Error)
	task := makeTask(userID, channelID, taskQuota, 0, BillingSourceWallet, 0)
	task.TaskID = "legacy-terminal-poll"
	task.Platform = constant.TaskPlatformSuno
	task.PrivateData.UpstreamTaskID = "legacy-upstream"
	task.SubmitTime = model.TaskRefundLegacyCutoff - 1
	task.Status = model.TaskStatusInProgress
	task.Progress = "50%"
	require.NoError(t, model.DB.Create(task).Error)

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		return &sunoFailurePollingAdaptor{failReason: "legacy failure"}
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, updateSunoTasks(context.Background(), channelID, []string{task.GetUpstreamTaskID()}, map[string]*model.Task{
		task.GetUpstreamTaskID(): task,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)
	assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID), "legacy rows must not be refunded")
	assert.Zero(t, countLogs(t))
}

func TestReadTaskPollingResponseHonorsContentLengthAndStreamLimit(t *testing.T) {
	old := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = old })

	_, err := ReadTaskPollingResponse(bytes.NewReader([]byte("{}")), 2<<20)
	assert.ErrorIs(t, err, errTaskPollingResponseTooLarge)
	_, err = ReadTaskPollingResponse(bytes.NewReader(bytes.Repeat([]byte{'x'}, 1<<20+1)), -1)
	assert.ErrorIs(t, err, errTaskPollingResponseTooLarge)
	body, err := ReadTaskPollingResponse(bytes.NewReader([]byte("ok")), -1)
	require.NoError(t, err)
	assert.Equal(t, []byte("ok"), body)
}

func TestReadTaskPollingResponseRejectsNilBody(t *testing.T) {
	_, err := ReadTaskPollingResponse(nil, -1)
	require.Error(t, err)
	var typedNil *bytes.Reader
	_, err = ReadTaskPollingResponse(typedNil, -1)
	require.Error(t, err)
}

func TestMaxTaskPollingResponseBytesUsesSafeDefaultForOverflow(t *testing.T) {
	previous := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = int(^uint(0) >> 1)
	t.Cleanup(func() { constant.MaxFileDownloadMB = previous })

	assert.Equal(t, common.DefaultMaxFileDownloadBytes, maxTaskPollingResponseBytes())
}

func TestRedactVideoResponseBodyDoesNotPersistMalformedProviderBody(t *testing.T) {
	malformed := []byte("not-json-" + strings.Repeat("x", 4096))
	redacted := redactVideoResponseBody(malformed)
	require.JSONEq(t, `{}`, string(redacted))
}

func TestRedactVideoResponseBodyRedactsNestedVideoPayloads(t *testing.T) {
	large := strings.Repeat("A", 1024)
	body := []byte(`{"response":{"bytesBase64Encoded":"` + large + `","video":"` + large + `","videos":[{"bytesBase64Encoded":"` + large + `","video":"` + large + `","mimeType":"video/mp4"}]},"api_key":"secret"}`)
	redacted := redactVideoResponseBody(body)
	var got map[string]any
	require.NoError(t, common.Unmarshal(redacted, &got))
	require.NotContains(t, got, "api_key")
	resp, ok := got["response"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, resp, "bytesBase64Encoded")
	assert.Equal(t, truncateBase64(large), resp["video"])
	videos, ok := resp["videos"].([]any)
	require.True(t, ok)
	require.Len(t, videos, 1)
	video, ok := videos[0].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, video, "bytesBase64Encoded")
	assert.Equal(t, truncateBase64(large), video["video"])
}

func TestTaskNeedsUpdateDoesNotTreatDifferentJSONValuesAsEqual(t *testing.T) {
	oldTask := &model.Task{
		Status:   model.TaskStatusInProgress,
		Progress: "50%",
		Data:     []byte(`{"a":1,"b":2}`),
	}
	newTask := taskdto.SunoDataResponse{
		Status: string(model.TaskStatusInProgress),
		Data:   []byte(`{"a":2,"b":1}`),
	}
	assert.True(t, taskNeedsUpdate(oldTask, newTask))

	newTask.Data = []byte(`{"b":2,"a":1}`)
	assert.False(t, taskNeedsUpdate(oldTask, newTask), "map key ordering alone should not trigger an update")
}
