package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type responsesDisconnectWriter struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
	once  sync.Once
}

// responsesGatedReader releases the first SSE event immediately and holds the
// tail until the test cancels the downstream request.  Returning the whole
// tail from one Read makes the error/completed ordering deterministic while
// still exercising the real bounded-drain path.
type responsesGatedReader struct {
	first   []byte
	tail    []byte
	release <-chan struct{}
	stage   int
}

func (r *responsesGatedReader) Read(p []byte) (int, error) {
	switch r.stage {
	case 0:
		r.stage++
		return copy(p, r.first), nil
	case 1:
		<-r.release
		r.stage++
		return copy(p, r.tail), nil
	default:
		return 0, io.EOF
	}
}

func (w *responsesDisconnectWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(data)
	if n > 0 {
		w.once.Do(func() { close(w.wrote) })
	}
	return n, err
}

func runDisconnectedResponsesStream(t *testing.T, event string) (*dto.Usage, *relaycommon.RelayInfo, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	responseWriter := &responsesDisconnectWriter{
		ResponseRecorder: httptest.NewRecorder(),
		wrote:            make(chan struct{}),
	}
	c, _ := gin.CreateTestContext(responseWriter)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	c.Set(common.RequestIdKey, "responses-disconnect-billing-test")
	info := &relaycommon.RelayInfo{
		OriginModelName: "test-model",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
	}
	info.SetEstimatePromptTokens(37)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	type result struct {
		usage  *dto.Usage
		apiErr *types.NewAPIError
	}
	done := make(chan result, 1)
	go func() {
		usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
		done <- result{usage: usage, apiErr: apiErr}
	}()

	_, err := io.WriteString(writer, "data: "+event+"\n\n")
	require.NoError(t, err)
	select {
	case <-responseWriter.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the streamed event")
	}
	cancel()

	select {
	case got := <-done:
		require.Nil(t, got.apiErr)
		require.NotNil(t, got.usage)
		return got.usage, info, c
	case <-time.After(2 * time.Second):
		t.Fatal("responses stream did not stop after client disconnect")
		return nil, nil, nil
	}
}

func TestOaiResponsesHandlerCountsOutputCallsNotDeclarations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("priced_fn", 5.0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest("priced_fn")
	})

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Tools: []map[string]any{
			{"type": "web_search_preview"},
			{"type": "file_search"},
		},
		Output: []dto.ResponsesOutput{
			{Type: dto.BuildInCallWebSearchCall},
			{Type: dto.BuildInCallWebSearchCall},
			{Type: dto.BuildInCallFunctionCall, Name: "priced_fn"},
			{Type: dto.BuildInCallFunctionCall, Name: "unpriced_fn"},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {ToolName: dto.BuildInToolWebSearchPreview, CallCount: 0},
				dto.BuildInToolFileSearch:       {ToolName: dto.BuildInToolFileSearch, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolFileSearch].CallCount)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, "priced_fn")
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["priced_fn"].CallCount)
	assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "unpriced_fn")
}

func TestOaiResponsesHandlerDeclaredToolsWithoutOutputCountZero(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Tools: []map[string]any{
			{"type": "web_search_preview"},
			{"type": "file_search"},
		},
		Output: []dto.ResponsesOutput{
			{Type: "message", Role: "assistant"},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {ToolName: dto.BuildInToolWebSearchPreview, CallCount: 0},
				dto.BuildInToolFileSearch:       {ToolName: dto.BuildInToolFileSearch, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolFileSearch].CallCount)
}

func TestOaiResponsesHandlerCountsCompletedImageGenerationOutputs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Status: []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_1",
				Status: "completed",
				Result: "base64-a",
			},
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_2",
				Status: "completed",
				Result: "base64-b",
			},
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_empty",
				Status: "completed",
				Result: "",
			},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-5.1"}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
	assert.False(t, c.GetBool("image_generation_call"))
}

func TestOaiResponsesHandlerIncompleteStatusCommitsZeroImageGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Status: []byte(`"incomplete"`),
		Output: []dto.ResponsesOutput{
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_1",
				Status: "completed",
				Result: "base64-a",
			},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolImageGeneration: {ToolName: dto.BuildInToolImageGeneration, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func runResponsesImageBillingStream(t *testing.T, events ...string) *relaycommon.RelayInfo {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	body.WriteString("data: [DONE]\n\n")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "responses-image-billing-test")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		DisablePing:     true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.1",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body.String())),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	return info
}

func TestOaiResponsesStreamHandlerDeduplicatesCompletedImageOutput(t *testing.T) {
	item := `{"type":"image_generation_call","id":"img_1","call_id":"call_1","status":"completed","result":"base64-a"}`
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`{"type":"response.completed","response":{"status":"completed","output":[`+item+`],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)

	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesStreamHandlerDiscardsImageOutputOnIncomplete(t *testing.T) {
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"img_1","status":"completed","result":"base64-a"}}`,
		`{"type":"response.incomplete","response":{"status":"incomplete"}}`,
	)

	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesStreamHandlerDoesNotCountPartialImageEvent(t *testing.T) {
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.image_generation_call.partial_image","output_index":0,"partial_image_b64":"partial-bytes"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
	)

	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesStreamHandlerDeduplicatesAddedAndDoneToolCall(t *testing.T) {
	item := `{"type":"web_search_call","id":"ws_1","status":"completed"}`
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.output_item.added","output_index":0,"item":`+item+`}`,
		`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)

	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolWebSearchPreview)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
}

func TestOaiResponsesStreamHandlerTreatsTopLevelErrorAsTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "responses-terminal-error-test")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"upstream_overloaded\",\"message\":\"upstream overloaded\"}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCode("upstream_overloaded"), apiErr.GetErrorCode())
	require.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
	require.Empty(t, w.Body.String())
}

func TestOaiResponsesStreamHandlerClientDisconnectBillsEstimatedInput(t *testing.T) {
	usage, info, c := runDisconnectedResponsesStream(
		t,
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
	)

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	assert.Equal(t, 37, usage.PromptTokens)
	assert.Equal(t, 0, usage.CompletionTokens)
	assert.Equal(t, 37, usage.TotalTokens)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiResponsesStreamHandlerClientDisconnectDrainsFinalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	responseWriter := &responsesDisconnectWriter{
		ResponseRecorder: httptest.NewRecorder(),
		wrote:            make(chan struct{}),
	}
	c, _ := gin.CreateTestContext(responseWriter)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(37)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	type result struct {
		usage  *dto.Usage
		apiErr *types.NewAPIError
	}
	done := make(chan result, 1)
	go func() {
		usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
		done <- result{usage: usage, apiErr: apiErr}
	}()

	_, err := io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	require.NoError(t, err)
	select {
	case <-responseWriter.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial event")
	}
	cancel()

	// The provider may have already computed usage when the downstream socket
	// disappears.  The Responses adapter must consume this short tail and use
	// the authoritative terminal usage instead of returning a zero bill.
	_, err = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":41,\"output_tokens\":9,\"total_tokens\":50}}}\n\n")
	require.NoError(t, err)
	select {
	case got := <-done:
		require.Nil(t, got.apiErr)
		require.NotNil(t, got.usage)
		assert.Equal(t, 41, got.usage.PromptTokens)
		assert.Equal(t, 9, got.usage.CompletionTokens)
		assert.Equal(t, 50, got.usage.TotalTokens)
		assert.Contains(t, []relaycommon.StreamEndReason{
			relaycommon.StreamEndReasonClientGone,
			relaycommon.StreamEndReasonDone,
			relaycommon.StreamEndReasonEOF,
		}, info.StreamStatus.EndReason,
			"the upstream terminal event may win the end-reason race after a full tail is drained")
	case <-time.After(3 * time.Second):
		t.Fatal("responses stream did not finish bounded drain")
	}
}

func TestOaiResponsesStreamHandlerClientDisconnectDrainsUsageAfterErrorEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	reader := &responsesGatedReader{
		first:   []byte("data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n"),
		tail:    []byte("data: {\"type\":\"response.error\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"server_error\",\"message\":\"failed after output\"}}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":41,\"output_tokens\":9,\"total_tokens\":50}}}\n\ndata: [DONE]\n\n"),
		release: release,
	}
	responseWriter := &responsesDisconnectWriter{
		ResponseRecorder: httptest.NewRecorder(),
		wrote:            make(chan struct{}),
	}
	c, _ := gin.CreateTestContext(responseWriter)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(37)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(reader),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	type result struct {
		usage  *dto.Usage
		apiErr *types.NewAPIError
	}
	done := make(chan result, 1)
	go func() {
		usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
		done <- result{usage: usage, apiErr: apiErr}
	}()

	select {
	case <-responseWriter.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial event")
	}
	cancel()
	close(release)

	select {
	case got := <-done:
		require.NotNil(t, got.apiErr, "the upstream error should still be returned")
		require.NotNil(t, got.usage,
			"a completed usage event after response.error must survive the drain")
		assert.Equal(t, 41, got.usage.PromptTokens)
		assert.Equal(t, 9, got.usage.CompletionTokens)
		assert.Equal(t, 50, got.usage.TotalTokens)
		assert.Contains(t, []relaycommon.StreamEndReason{
			relaycommon.StreamEndReasonClientGone,
			relaycommon.StreamEndReasonDone,
			relaycommon.StreamEndReasonEOF,
		}, info.StreamStatus.EndReason,
			"the upstream terminal event may win the end-reason race after a full tail is drained")
	case <-time.After(3 * time.Second):
		t.Fatal("responses stream did not finish bounded drain")
	}
}

func TestOaiResponsesStreamHandlerClientDisconnectDrainsWithPingEnabled(t *testing.T) {
	// Keep the ping worker enabled: its cancellation defer must not close the
	// scanner stop channel before the Responses adapter has consumed the
	// bounded upstream tail.
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	oldGeneral := operation_setting.GetGeneralSettingSnapshot()
	operation_setting.UpdateGeneralSetting(func(setting *operation_setting.GeneralSetting) {
		setting.PingIntervalEnabled = true
		setting.PingIntervalSeconds = 1
	})
	t.Cleanup(func() {
		operation_setting.UpdateGeneralSetting(func(setting *operation_setting.GeneralSetting) {
			*setting = oldGeneral
		})
	})

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	responseWriter := &responsesDisconnectWriter{
		ResponseRecorder: httptest.NewRecorder(),
		wrote:            make(chan struct{}),
	}
	c, _ := gin.CreateTestContext(responseWriter)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		// Do not disable ping in this regression case.
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(37)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	type result struct {
		usage  *dto.Usage
		apiErr *types.NewAPIError
	}
	done := make(chan result, 1)
	go func() {
		usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
		done <- result{usage: usage, apiErr: apiErr}
	}()

	_, err := io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	require.NoError(t, err)
	select {
	case <-responseWriter.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial event")
	}
	cancel()
	_, err = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":41,\"output_tokens\":9,\"total_tokens\":50}}}\n\n")
	require.NoError(t, err)

	select {
	case got := <-done:
		require.Nil(t, got.apiErr)
		require.NotNil(t, got.usage)
		assert.Equal(t, 41, got.usage.PromptTokens)
		assert.Equal(t, 9, got.usage.CompletionTokens)
		assert.Equal(t, 50, got.usage.TotalTokens)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	case <-time.After(3 * time.Second):
		t.Fatal("responses stream did not finish bounded drain with ping enabled")
	}
}

func TestOaiResponsesStreamHandlerClientDisconnectCountsReasoningOutput(t *testing.T) {
	usage, info, c := runDisconnectedResponsesStream(
		t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"reasoning work already generated"}`,
	)

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	assert.Equal(t, 37, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiResponsesStreamHandlerClientDisconnectCountsFunctionArguments(t *testing.T) {
	usage, _, c := runDisconnectedResponsesStream(
		t,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"query\":\"billable work\"}"}`,
	)

	assert.Equal(t, 37, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiResponsesStreamHandlerClientDisconnectCountsDoneOnlyFunctionArguments(t *testing.T) {
	usage, _, c := runDisconnectedResponsesStream(
		t,
		`{"type":"response.function_call_arguments.done","item_id":"fc_done_1","output_index":0,"arguments":"{\"query\":\"done-only\"}"}`,
	)

	assert.Equal(t, 37, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens,
		"a done-only function-call event still represents generated output")
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestOaiResponsesStreamHandlerDoesNotDoubleCountFunctionArgumentsDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_dedupe","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_dedupe","output_index":0,"arguments":"{\"q\":\"x\"}"}`,
		`data: [DONE]`,
		"",
	}, "\n")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(1)
	service.InitTokenEncoders()
	usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	assert.Less(t, usage.CompletionTokens, 10,
		"the full done value must not be added a second time after its delta")
}

func TestResponsesObservedOutputMergesDeltaAndDoneWithoutDuplication(t *testing.T) {
	observed := newResponsesObservedOutput()
	observed.observeDelta("function_arguments", "item:fc_1", `{"q":"x"}`)
	observed.observeComplete("function_arguments", "item:fc_1", `{"q":"x"}`)
	assert.Equal(t, `{"q":"x"}`+"\n", observed.String())
}

func TestOaiResponsesStreamHandlerIgnoresBase64AudioForTextEstimate(t *testing.T) {
	usage, _, _ := runDisconnectedResponsesStream(
		t,
		`{"type":"response.audio.delta","item_id":"audio_1","delta":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`,
	)

	assert.Zero(t, usage.CompletionTokens,
		"raw base64 audio must not be passed through a text tokenizer")
}

func TestOaiResponsesStreamHandlerReturnsPartialUsageOnFailedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"partial output"}`,
		`data: {"type":"response.failed","response":{"status":"failed","usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15},"error":{"type":"server_error","message":"failed after output"}}}`,
		"",
	}, "\n")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(3)
	usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.NotNil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Equal(t, 4, usage.CompletionTokens)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyResponsesPartialUsage))
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyResponsesStreamIncomplete))
}

func TestOaiResponsesStreamHandlerReturnsPartialUsageOnResponseErrorEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","item_id":"msg_error_1","delta":"partial output"}`,
		`data: {"type":"response.error","response":{"status":"failed","usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18},"error":{"type":"server_error","message":"failed after output"}}}`,
		"",
	}, "\n")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(3)
	usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.NotNil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 13, usage.PromptTokens)
	assert.Equal(t, 5, usage.CompletionTokens)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyResponsesPartialUsage))
}

func TestOaiResponsesStreamHandlerReturnsTopLevelUsageOnResponseErrorEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.error","usage":{"input_tokens":17,"output_tokens":6,"total_tokens":23},"error":{"type":"server_error","message":"top-level usage"}}`,
		"",
	}, "\n")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(3)
	usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.NotNil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 17, usage.PromptTokens)
	assert.Equal(t, 6, usage.CompletionTokens)
	assert.Equal(t, 23, usage.TotalTokens)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyResponsesPartialUsage))
}

func TestOaiResponsesStreamHandlerBillsOutputEmbeddedInFailedEventWithoutUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	operation_setting.SetToolPriceForTest("failed_lookup", 5)
	t.Cleanup(func() { operation_setting.DeleteToolPriceForTest("failed_lookup") })

	body := strings.Join([]string{
		`data: {"type":"response.failed","response":{"status":"failed","output":[{"type":"function_call","id":"fc_failed","name":"failed_lookup","arguments":"{\"q\":\"x\"}"}]}}`,
		"",
	}, "\n")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(3)
	usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.NotNil(t, apiErr)
	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens,
		"embedded output must be retained when failed event omits usage")
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyResponsesPartialUsage))
	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, "failed_lookup")
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["failed_lookup"].CallCount)
}

func TestOaiResponsesStreamHandlerCountsParallelAnonymousSameNameTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("lookup", 5)
	t.Cleanup(func() { operation_setting.DeleteToolPriceForTest("lookup") })
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"lookup"}}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"lookup"}}`,
		`data: [DONE]`,
		"",
	}, "\n")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	_, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})
	require.Nil(t, apiErr)
	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, "lookup")
	assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools["lookup"].CallCount)
}

func TestOaiResponsesStreamHandlerClientDisconnectKeepsStartedToolCall(t *testing.T) {
	_, info, _ := runDisconnectedResponsesStream(
		t,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"in_progress"}}`,
	)

	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolWebSearchPreview)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
}

func TestOaiResponsesStreamHandlerClientDisconnectKeepsAuthoritativeUsage(t *testing.T) {
	usage, _, c := runDisconnectedResponsesStream(
		t,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}`,
	)

	assert.Equal(t, 11, usage.PromptTokens)
	assert.Equal(t, 7, usage.CompletionTokens)
	assert.Equal(t, 18, usage.TotalTokens)
	assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestApplyResponsesUsageForEventMergesInputOnlyTerminalUsage(t *testing.T) {
	target := &dto.Usage{PromptTokens: 37, CompletionTokens: 123, TotalTokens: 160}
	partial := &dto.Usage{InputTokens: 11, TotalTokens: 11}

	require.True(t, applyResponsesUsageForEvent(target, partial, "response.incomplete"))
	assert.Equal(t, 11, target.PromptTokens)
	assert.Equal(t, 123, target.CompletionTokens,
		"input-only cancellation usage must not erase output observed before cancellation")
	assert.Equal(t, 134, target.TotalTokens)
}

func TestApplyResponsesUsageForEventMergesOutputOnlyTerminalUsage(t *testing.T) {
	target := &dto.Usage{PromptTokens: 37, CompletionTokens: 123, TotalTokens: 160}
	partial := &dto.Usage{OutputTokens: 8, TotalTokens: 8}

	require.True(t, applyResponsesUsageForEvent(target, partial, "response.cancelled"))
	assert.Equal(t, 37, target.PromptTokens,
		"output-only cancellation usage must not erase the request input")
	assert.Equal(t, 8, target.CompletionTokens)
	assert.Equal(t, 45, target.TotalTokens)
}

func TestApplyResponsesUsageForEventAllowsZeroOutputOnCompletedResponse(t *testing.T) {
	// No locally observed output: an explicit completed usage object with zero
	// output is valid for a tool-only response and should replace the target.
	target := &dto.Usage{PromptTokens: 37, CompletionTokens: 0, TotalTokens: 37}
	complete := &dto.Usage{InputTokens: 11, OutputTokens: 0, TotalTokens: 11}

	require.True(t, applyResponsesUsageForEvent(target, complete, "response.completed"))
	assert.Equal(t, 11, target.PromptTokens)
	assert.Zero(t, target.CompletionTokens)
	assert.Equal(t, 11, target.TotalTokens)
}

func TestApplyResponsesUsageForEventKeepsObservedOutputWhenCompletedOmitsIt(t *testing.T) {
	target := &dto.Usage{PromptTokens: 37, CompletionTokens: 123, TotalTokens: 160}
	complete := &dto.Usage{InputTokens: 11, TotalTokens: 11}

	require.True(t, applyResponsesUsageForEvent(target, complete, "response.completed"))
	assert.Equal(t, 11, target.PromptTokens)
	assert.Equal(t, 123, target.CompletionTokens,
		"a completed event that omits output must not erase observed reasoning/text")
	assert.Equal(t, 134, target.TotalTokens)
}

func TestResponsesUsageIsAuthoritativeTerminalAcceptsNativeInputOutputFields(t *testing.T) {
	source := &dto.Usage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18}
	assert.True(t, responsesUsageIsAuthoritativeTerminal("response.completed", source))
	assert.True(t, responsesUsageIsAuthoritativeTerminal("response.incomplete", source))
}

func TestOaiResponsesStreamHandlerMarksEOFWithoutTerminalAsIncomplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	info.SetEstimatePromptTokens(23)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`data: {"type":"response.created","response":{"status":"in_progress"}}

`)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyResponsesStreamIncomplete))
	assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyResponsesStreamTerminalSeen))
	assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
}
