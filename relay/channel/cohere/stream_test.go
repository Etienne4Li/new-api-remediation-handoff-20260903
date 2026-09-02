package cohere

import (
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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cohereCloseNotifyRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func (r *cohereCloseNotifyRecorder) CloseNotify() <-chan bool {
	return r.closed
}

func newCohereStreamTestContext(t *testing.T, body string) (*gin.Context, *cohereCloseNotifyRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := &cohereCloseNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "cohere-stream-test")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "command-r"},
		IsStream:    true,
	}
	return c, recorder, resp, info
}

func TestCohereStreamHandlerConvertsV1SSEAndUsesBilledUsage(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		"event: text-generation",
		`data: {"is_finished":false,"event_type":"text-generation","text":"hello "}`,
		"",
		"event: text-generation",
		`data: {"is_finished":false,"event_type":"text-generation","text":"world"}`,
		"",
		"event: stream-end",
		`data: {"is_finished":true,"event_type":"stream-end","finish_reason":"COMPLETE","response":{"response_id":"upstream-1","text":"hello world","finish_reason":"COMPLETE","meta":{"billed_units":{"input_tokens":7,"output_tokens":3}}}}`,
		"",
	}, "\n")
	c, recorder, resp, info := newCohereStreamTestContext(t, body)

	usage, apiErr := cohereStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 10, usage.TotalTokens)
	got := recorder.Body.String()
	assert.Contains(t, got, `"content":"hello "`)
	assert.Contains(t, got, `"content":"world"`)
	assert.Contains(t, got, `"finish_reason":"stop"`)
	assert.Contains(t, got, "data: [DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Contains(t, []relaycommon.StreamEndReason{
		relaycommon.StreamEndReasonDone,
		relaycommon.StreamEndReasonEOF,
	}, info.StreamStatus.EndReason)
}

func TestCohereStreamHandlerConvertsV2SSEEventsAndNestedUsage(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		"event: message-start",
		`data: {"id":"message-1","type":"message-start","delta":{"message":{"role":"assistant"}}}`,
		"",
		"event: content-start",
		`data: {"type":"content-start","index":0,"delta":{"message":{"content":{"type":"text","text":""}}}}`,
		"",
		"event: content-delta",
		`data: {"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"hello"}}}}`,
		"",
		"event: message-end",
		`data: {"type":"message-end","delta":{"finish_reason":"COMPLETE","usage":{"billed_units":{"input_tokens":11,"output_tokens":4}}}}`,
		"",
	}, "\n")
	c, recorder, resp, info := newCohereStreamTestContext(t, body)

	usage, apiErr := cohereStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Equal(t, 4, usage.CompletionTokens)
	assert.Equal(t, 15, usage.TotalTokens)
	got := recorder.Body.String()
	assert.Contains(t, got, `"content":"hello"`)
	assert.Contains(t, got, `"finish_reason":"stop"`)
	assert.Contains(t, got, "data: [DONE]")
}

type cohereBlockingBody struct {
	mu     sync.Mutex
	first  []byte
	sent   bool
	closed chan struct{}
}

func (b *cohereBlockingBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if !b.sent {
		b.sent = true
		n := copy(p, b.first)
		b.mu.Unlock()
		return n, nil
	}
	b.mu.Unlock()
	<-b.closed
	return 0, io.EOF
}

func (b *cohereBlockingBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

type cohereCancelWriter struct {
	gin.ResponseWriter
	needle string
	cancel context.CancelFunc
	once   sync.Once
}

func (w *cohereCancelWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if strings.Contains(string(p), w.needle) {
		w.once.Do(w.cancel)
	}
	return n, err
}

func (w *cohereCancelWriter) WriteString(s string) (int, error) {
	n, err := io.WriteString(w.ResponseWriter, s)
	if strings.Contains(s, w.needle) {
		w.once.Do(w.cancel)
	}
	return n, err
}

func TestCohereStreamHandlerClosesUpstreamOnClientCancellation(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first := []byte("event: text-generation\ndata: {\"is_finished\":false,\"event_type\":\"text-generation\",\"text\":\"hello\"}\n\n")
	body := &cohereBlockingBody{first: first, closed: make(chan struct{})}
	recorder := &cohereCloseNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestCtx)
	c.Set(common.RequestIdKey, "cohere-disconnect-test")
	c.Writer = &cohereCancelWriter{ResponseWriter: c.Writer, needle: `"content":"hello"`, cancel: cancel}
	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "command-r"},
		IsStream:    true,
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}

	done := make(chan struct{})
	var usage *dto.Usage
	var apiErr any
	go func() {
		usage, apiErr = cohereStreamHandler(c, info, resp)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cohere stream handler did not stop after client cancellation")
	}
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	select {
	case <-body.closed:
	default:
		t.Fatal("upstream response body was not closed")
	}
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
}
