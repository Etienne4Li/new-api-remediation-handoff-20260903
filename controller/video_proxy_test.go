package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetGeminiVideoURLPrefersPrivateSignedURLOverRedactedTaskData(t *testing.T) {
	task := &model.Task{
		TaskID: "task-gemini-1",
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://storage.example/video.mp4?X-Goog-Signature=private",
		},
		// The persisted public projection has had the signature removed. Using it
		// would make the subsequent media fetch fail with 403.
		Data: []byte(`{"response":{"videos":[{"uri":"https://storage.example/video.mp4"}]}}`),
	}

	got, err := getGeminiVideoURL(&model.Channel{}, task, "AIza-test")
	require.NoError(t, err)
	assert.Equal(t, "https://storage.example/video.mp4?X-Goog-Signature=private", got)
}

func TestGetGeminiVideoURLPreservesCrossOriginSignedQueryAndOmitsAPIKeyHeader(t *testing.T) {
	const (
		apiKey    = "AIza-test"
		signedURL = "https://storage.example/video.mp4?X-Goog-Signature=abc%2Fdef%2Bghi&key=storage-signature&part=1&part=2&empty=&space=a+b"
	)
	task := &model.Task{
		TaskID: "task-gemini-cross-origin-signed-query",
		PrivateData: model.TaskPrivateData{
			ResultURL: signedURL,
		},
	}
	channel := &model.Channel{Type: constant.ChannelTypeGemini}

	got, err := getGeminiVideoURL(channel, task, apiKey)
	require.NoError(t, err)
	assert.Equal(t, signedURL, got, "cross-origin signed query bytes must remain unchanged")

	req, err := http.NewRequest(http.MethodGet, got, nil)
	require.NoError(t, err)
	if shouldAttachGeminiAPIKey(geminiVideoAPIBaseURL(channel), got) {
		req.Header.Set("x-goog-api-key", apiKey)
	}
	assert.Empty(t, req.Header.Get("x-goog-api-key"))
}

func TestVideoProxyReloadsGeminiSignedURLAndPreservesExactCrossOriginQuery(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}))

	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	previousSSRFProtection := system_setting.GetFetchSetting().EnableSSRFProtection
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
		_ = config.GlobalConfig.LoadFromDB(map[string]string{
			"fetch_setting.enable_ssrf_protection": fmt.Sprintf("%t", previousSSRFProtection),
		})
		service.InitHttpClient()
	})
	common.CryptoSecret = "gemini-video-proxy-round-trip-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	common.MemoryCacheEnabled = false
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"fetch_setting.enable_ssrf_protection": "false",
	}))
	service.InitHttpClient()

	const signedQuery = "X-Goog-Signature=abc%2Fdef%2Bghi&key=storage-signature&part=1&part=2&empty=&space=a+b"
	var receivedRawQuery string
	var receivedRequestURI string
	var receivedAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRawQuery = r.URL.RawQuery
		receivedRequestURI = r.RequestURI
		receivedAPIKey = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(testVideoMP4Bytes())
	}))
	defer upstream.Close()

	channel := &model.Channel{
		Type:   constant.ChannelTypeGemini,
		Key:    "gemini-channel-key",
		Name:   "gemini-video-proxy-round-trip",
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(channel).Error)

	const (
		ownerID = 7101
		taskID  = "task_gemini_signed_query_round_trip"
		apiKey  = "AIza-private-task-key"
	)
	signedURL := upstream.URL + "/generated/video.mp4?" + signedQuery
	task := &model.Task{
		TaskID:    taskID,
		Platform:  constant.TaskPlatform("video"),
		UserId:    ownerID,
		ChannelId: channel.Id,
		Status:    model.TaskStatusSuccess,
		Progress:  "100%",
		PrivateData: model.TaskPrivateData{
			Key:       apiKey,
			ResultURL: signedURL,
		},
	}
	require.NoError(t, db.Create(task).Error)
	var rawPrivateData string
	require.NoError(t, db.Table("tasks").Select("private_data").Where("id = ?", task.ID).Scan(&rawPrivateData).Error)
	assert.NotContains(t, rawPrivateData, signedQuery)
	assert.NotContains(t, rawPrivateData, apiKey)

	engine := gin.New()
	engine.GET("/v1/videos/:task_id/content", func(c *gin.Context) {
		c.Set("id", ownerID)
	}, VideoProxy)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/videos/"+taskID+"/content", nil)
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, testVideoMP4Bytes(), recorder.Body.Bytes())
	assert.Equal(t, signedQuery, receivedRawQuery)
	assert.Equal(t, "/generated/video.mp4?"+signedQuery, receivedRequestURI)
	assert.Empty(t, receivedAPIKey)
}

func TestGetGeminiVideoURLMovesSameOriginCredentialToAPIKeyHeader(t *testing.T) {
	const apiKey = "AIza-current"
	task := &model.Task{
		TaskID: "task-gemini-same-origin-api-url",
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://generativelanguage.googleapis.com/v1/video.mp4?key=stale&download=1",
		},
	}
	channel := &model.Channel{Type: constant.ChannelTypeGemini}

	got, err := getGeminiVideoURL(channel, task, apiKey)
	require.NoError(t, err)
	assert.Equal(t, "https://generativelanguage.googleapis.com/v1/video.mp4?download=1", got)

	req, err := http.NewRequest(http.MethodGet, got, nil)
	require.NoError(t, err)
	if shouldAttachGeminiAPIKey(geminiVideoAPIBaseURL(channel), got) {
		req.Header.Set("x-goog-api-key", apiKey)
	}
	assert.Equal(t, apiKey, req.Header.Get("x-goog-api-key"))
}

func TestValidateVideoProxyResponseIdentity(t *testing.T) {
	expectedName := "projects/test/locations/us-central1/models/veo/operations/expected"
	expectedID := taskcommon.EncodeLocalTaskID(expectedName)
	task := &model.Task{
		TaskID: "task_public_identity",
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: expectedID,
		},
	}

	tests := []struct {
		name     string
		parsedID string
		body     string
		wantErr  bool
	}{
		{
			name:     "both identities match",
			parsedID: expectedID,
			body:     `{"name":"` + expectedName + `"}`,
		},
		{
			name: "identity omitted for legacy response",
			body: `{}`,
		},
		{
			name:     "parser identity mismatch",
			parsedID: taskcommon.EncodeLocalTaskID(expectedName + "-other"),
			body:     `{"name":"` + expectedName + `"}`,
			wantErr:  true,
		},
		{
			name:     "raw identity mismatch even when parser matches",
			parsedID: expectedID,
			body:     `{"name":"` + expectedName + `-other"}`,
			wantErr:  true,
		},
		{
			name:    "non string raw identity",
			body:    `{"name":{"operation":"expected"}}`,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateVideoProxyResponseIdentity(task, test.parsedID, []byte(test.body))
			if test.wantErr {
				require.ErrorIs(t, err, service.ErrTaskPollingIdentityMismatch)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestGetGeminiVideoURLRejectsMismatchedPersistedTaskData(t *testing.T) {
	expectedName := "projects/test/locations/us-central1/models/veo/operations/expected"
	task := &model.Task{
		TaskID: "task_public_gemini_persisted_mismatch",
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: taskcommon.EncodeLocalTaskID(expectedName),
		},
		Data: []byte(`{"name":"projects/test/locations/us-central1/models/veo/operations/other","done":true,"response":{"videos":[{"uri":"https://legacy.example/video.mp4"}]}}`),
	}

	_, err := getGeminiVideoURL(&model.Channel{Type: constant.ChannelTypeGemini}, task, "AIza-test")
	require.ErrorIs(t, err, service.ErrTaskPollingIdentityMismatch)
}

func TestGetVertexVideoURLRejectsMismatchedPersistedTaskData(t *testing.T) {
	expectedName := "projects/test/locations/us-central1/models/veo/operations/expected"
	task := &model.Task{
		TaskID: "task_public_vertex_persisted_mismatch",
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: taskcommon.EncodeLocalTaskID(expectedName),
		},
		Data: []byte(`{"name":"projects/test/locations/us-central1/models/veo/operations/other","done":true,"response":{"video":"https://legacy.example/video.mp4"}}`),
	}

	_, err := getVertexVideoURL(&model.Channel{Type: constant.ChannelTypeVertexAi}, task)
	require.ErrorIs(t, err, service.ErrTaskPollingIdentityMismatch)
}

func TestGetGeminiVideoURLPrefersLiveSignedURLOverLegacyRedactedURL(t *testing.T) {
	operationName := "projects/test/locations/us-central1/models/veo/operations/live-signed"
	liveURL := "https://storage.example/video.mp4?X-Goog-Signature=live"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "AIza-test", r.Header.Get("x-goog-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"`+operationName+`","done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"`+liveURL+`"}}]}}}`)
	}))
	defer server.Close()

	baseURL := server.URL
	task := &model.Task{
		TaskID: "task_public_live_signed",
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: taskcommon.EncodeLocalTaskID(operationName),
		},
		FailReason: "https://storage.example/video.mp4",
		Data:       []byte(`{"name":"` + operationName + `","done":true,"response":{"videos":[{"uri":"https://storage.example/video.mp4"}]}}`),
	}
	channel := &model.Channel{Type: constant.ChannelTypeGemini, BaseURL: &baseURL}

	got, err := getGeminiVideoURL(channel, task, "AIza-test")
	require.NoError(t, err)
	assert.Equal(t, liveURL, got)
	assert.EqualValues(t, 1, requests.Load())
}

func TestGetGeminiVideoURLRejectsMismatchedLiveOperationWithoutLegacyFallback(t *testing.T) {
	expectedName := "projects/test/locations/us-central1/models/veo/operations/expected-live"
	otherName := "projects/test/locations/us-central1/models/veo/operations/other-live"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"`+otherName+`","done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"https://storage.example/other.mp4?signature=private"}}]}}}`)
	}))
	defer server.Close()

	baseURL := server.URL
	task := &model.Task{
		TaskID: "task_public_live_mismatch",
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: taskcommon.EncodeLocalTaskID(expectedName),
		},
		FailReason: "https://legacy.example/fail-reason.mp4",
		Data:       []byte(`{"name":"` + expectedName + `","done":true,"response":{"videos":[{"uri":"https://legacy.example/redacted.mp4"}]}}`),
	}
	channel := &model.Channel{Type: constant.ChannelTypeGemini, BaseURL: &baseURL}

	_, err := getGeminiVideoURL(channel, task, "AIza-test")
	require.ErrorIs(t, err, service.ErrTaskPollingIdentityMismatch)
	assert.EqualValues(t, 1, requests.Load())
}

func testVideoMP4Bytes() []byte {
	// A minimal ISO Base Media ftyp box. The marker is enough for the MIME
	// sniffer; the payload is deliberately not used as a playable fixture.
	return []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2',
		0x00, 0x00, 0x00, 0x00, 'm', 'p', '4', '2', 'i', 's', 'o', 'm',
	}
}

func TestSpoolVideoProxyBodyEnforcesHardLimit(t *testing.T) {
	probe := testVideoMP4Bytes()
	tail := bytes.Repeat([]byte{'x'}, 128)
	spooled, size, err := spoolVideoProxyBody(bytes.NewReader(tail), probe, int64(len(probe)+len(tail)))
	require.NoError(t, err)
	defer func() {
		name := spooled.Name()
		_ = spooled.Close()
		_ = os.Remove(name)
	}()
	got, err := io.ReadAll(spooled)
	require.NoError(t, err)
	assert.Equal(t, int64(len(probe)+len(tail)), size)
	assert.Equal(t, append(append([]byte(nil), probe...), tail...), got)

	_, _, err = spoolVideoProxyBody(bytes.NewReader(bytes.Repeat([]byte{'x'}, 129)), probe, int64(len(probe)+len(tail)))
	assert.ErrorIs(t, err, errVideoProxyBodyTooLarge)
}

func TestSniffVideoProxyMIME(t *testing.T) {
	avi := []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'A', 'V', 'I', ' '}
	flv := []byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 9}
	webm := []byte{0x1a, 0x45, 0xdf, 0xa3, 0x93, 0x42, 0x82, 0x88}
	quickTime := []byte{0, 0, 0, 0x10, 'f', 't', 'y', 'p', 'q', 't', ' ', ' ', 0, 0, 0, 0}

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "mp4", data: testVideoMP4Bytes(), want: "video/mp4"},
		{name: "webm", data: webm, want: "video/webm"},
		{name: "avi", data: avi, want: "video/avi"},
		{name: "flv", data: flv, want: "video/flv"},
		{name: "quicktime", data: quickTime, want: "video/quicktime"},
		{name: "html", data: []byte("<html><script>alert(1)</script></html>"), want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, sniffVideoProxyMIME(test.data))
		})
	}
}

func TestResolveVideoProxyContentTypeRequiresMatchingSafeContainer(t *testing.T) {
	mp4 := testVideoMP4Bytes()
	webm := []byte{0x1a, 0x45, 0xdf, 0xa3}

	tests := []struct {
		name     string
		declared string
		probe    []byte
		want     string
		wantErr  bool
	}{
		{name: "canonical declared type", declared: "video/mp4; codecs=avc1", probe: mp4, want: "video/mp4"},
		{name: "infer missing type", declared: "", probe: mp4, want: "video/mp4"},
		{name: "reject html type", declared: "text/html", probe: mp4, wantErr: true},
		{name: "reject mismatched container", declared: "video/mp4", probe: webm, wantErr: true},
		{name: "reject unknown type", declared: "application/octet-stream", probe: mp4, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveVideoProxyContentType(test.declared, test.probe)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestDecodeVideoProxyDataURLEnforcesTypeAndEncoding(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 64
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	mp4 := testVideoMP4Bytes()
	encoded := base64.StdEncoding.EncodeToString(mp4)
	gotType, gotBytes, err := decodeVideoProxyDataURL("DATA:VIDEO/MP4;BASE64," + encoded)
	require.NoError(t, err)
	assert.Equal(t, "video/mp4", gotType)
	assert.Equal(t, mp4, gotBytes)

	_, _, err = decodeVideoProxyDataURL("data:;base64," + encoded)
	require.NoError(t, err, "a missing media type is inferred from a valid container")

	invalid := []string{
		"data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte("<html>")),
		"data:video/mp4," + encoded,
		"data:video/mp4;charset=utf-8;base64," + encoded,
		"data:video/mp4;base64;base64," + encoded,
		"data:video/mp4;base64,not-base64!",
		"data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("not a video")),
	}
	for _, dataURL := range invalid {
		_, _, err := decodeVideoProxyDataURL(dataURL)
		assert.Error(t, err, dataURL)
	}
}

func TestDecodeVideoProxyDataURLRejectsOversizedPayloadBeforeDecode(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	// Keep a valid MP4 prefix so the failure proves the size gate, rather than
	// merely the magic-byte check.
	data := append(testVideoMP4Bytes(), bytes.Repeat([]byte{0}, (1<<20)+1-len(testVideoMP4Bytes()))...)
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(data)
	_, _, err := decodeVideoProxyDataURL(dataURL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum size")
}

func TestSetVideoProxyResponseHeadersDropsUpstreamPolicyHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	for _, key := range videoProxyVideoHeaderBlocklist {
		c.Writer.Header().Set(key, "attacker-controlled")
	}

	setVideoProxyResponseHeaders(c, "video/mp4", 24)

	assert.Equal(t, "video/mp4", c.Writer.Header().Get("Content-Type"))
	assert.Equal(t, "24", c.Writer.Header().Get("Content-Length"))
	assert.Equal(t, "private, no-store, max-age=0", c.Writer.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", c.Writer.Header().Get("Pragma"))
	assert.Equal(t, "nosniff", c.Writer.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", c.Writer.Header().Get("Referrer-Policy"))
	assert.Equal(t, `default-src 'none'; sandbox`, c.Writer.Header().Get("Content-Security-Policy"))
	assert.Equal(t, `inline; filename="video"`, c.Writer.Header().Get("Content-Disposition"))
	assert.Empty(t, c.Writer.Header().Get("Set-Cookie"))
	assert.Empty(t, c.Writer.Header().Get("Content-Encoding"))
	assert.Empty(t, c.Writer.Header().Get("Content-Range"))
	assert.Empty(t, c.Writer.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, c.Writer.Header().Get("Access-Control-Allow-Credentials"))
}

func TestReadVideoProxyTaskResponseRejectsAdvertisedOversize(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	limit := videoProxyTaskResponseMaxBytes()
	resp := &http.Response{
		ContentLength: limit + 1,
		Body:          io.NopCloser(bytes.NewReader([]byte(`{"done":true}`))),
	}
	body, err := readVideoProxyTaskResponse(resp)
	require.ErrorIs(t, err, errVideoProxyTaskResponseTooLarge)
	assert.Nil(t, body)
}

func TestSanitizeGeminiVideoURLOnlyRemovesSameOriginCredentialQuery(t *testing.T) {
	const apiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{name: "plain URL", uri: "https://generativelanguage.googleapis.com/v1/video.mp4", want: "https://generativelanguage.googleapis.com/v1/video.mp4"},
		{name: "preserve unrelated same-origin query", uri: "https://generativelanguage.googleapis.com/v1/video.mp4?download=1", want: "https://generativelanguage.googleapis.com/v1/video.mp4?download=1"},
		{name: "preserve exact same-origin signed query", uri: "https://generativelanguage.googleapis.com/v1/video.mp4?z=last&X-Goog-Signature=abc%2fdef%2Bghi&part=2&part=1&empty=&space=a+b", want: "https://generativelanguage.googleapis.com/v1/video.mp4?z=last&X-Goog-Signature=abc%2fdef%2Bghi&part=2&part=1&empty=&space=a+b"},
		{name: "remove same-origin key query", uri: "https://generativelanguage.googleapis.com/v1/video.mp4?key=old&download=1", want: "https://generativelanguage.googleapis.com/v1/video.mp4?download=1"},
		{name: "remove same-origin case variants", uri: "https://generativelanguage.googleapis.com/v1/video.mp4?API_KEY=old&x-goog-api-key=old2", want: "https://generativelanguage.googleapis.com/v1/video.mp4"},
		{name: "preserve cross-origin key-like signature", uri: "https://storage.example/video.mp4?key=storage-signature&part=a%2Fb", want: "https://storage.example/video.mp4?key=storage-signature&part=a%2Fb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeGeminiVideoURL(apiBaseURL, tt.uri)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShouldAttachGeminiAPIKeyOnlyForSameOrigin(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		media string
		want  bool
	}{
		{
			name:  "same origin",
			base:  "https://generativelanguage.googleapis.com/v1beta",
			media: "https://generativelanguage.googleapis.com/v1/video.mp4",
			want:  true,
		},
		{
			name:  "default https port is equivalent",
			base:  "https://generativelanguage.googleapis.com",
			media: "https://generativelanguage.googleapis.com:443/video.mp4",
			want:  true,
		},
		{
			name:  "storage host",
			base:  "https://generativelanguage.googleapis.com",
			media: "https://storage.googleapis.com/video.mp4?signature=private",
			want:  false,
		},
		{
			name:  "different scheme",
			base:  "https://generativelanguage.googleapis.com",
			media: "http://generativelanguage.googleapis.com/video.mp4",
			want:  false,
		},
		{
			name:  "userinfo is rejected",
			base:  "https://generativelanguage.googleapis.com",
			media: "https://user:pass@generativelanguage.googleapis.com/video.mp4",
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldAttachGeminiAPIKey(tt.base, tt.media))
		})
	}
}

func TestVideoProxyClientWithRedirectGuardStripsCredentialsAcrossOrigins(t *testing.T) {
	var received struct {
		authorization string
		gemini        string
		referer       string
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.authorization = r.Header.Get("Authorization")
		received.gemini = r.Header.Get("x-goog-api-key")
		received.referer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(testVideoMP4Bytes())
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()

	client := videoProxyClientWithRedirectGuard(&http.Client{})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, redirect.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("x-goog-api-key", "AIza-secret")
	req.Header.Set("Referer", "https://private.example/video?signature=secret")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, received.authorization)
	assert.Empty(t, received.gemini)
	assert.Empty(t, received.referer)
}

func TestVideoProxyClientWithRedirectGuardPreservesCredentialsSameOrigin(t *testing.T) {
	var received string
	server := httptest.NewServer(nil)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Location", server.URL+"/final")
			w.WriteHeader(http.StatusFound)
		case "/final":
			received = r.Header.Get("x-goog-api-key")
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testVideoMP4Bytes())
		}
	})
	defer server.Close()

	client := videoProxyClientWithRedirectGuard(&http.Client{})
	req, err := http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	require.NoError(t, err)
	req.Header.Set("x-goog-api-key", "AIza-same-origin")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "AIza-same-origin", received)
}

func TestSameVideoProxyOrigin(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "same origin", left: "https://example.test:443/a", right: "https://EXAMPLE.test/b", want: true},
		{name: "userinfo on left", left: "https://user:pass@example.test/a", right: "https://example.test/b", want: false},
		{name: "userinfo on right", left: "https://example.test/a", right: "https://user:pass@example.test/b", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := url.Parse(test.left)
			require.NoError(t, err)
			right, err := url.Parse(test.right)
			require.NoError(t, err)
			assert.Equal(t, test.want, sameVideoProxyOrigin(left, right))
		})
	}
}

func TestReadVideoProxyTaskResponseBoundsUnknownLength(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	limit := videoProxyTaskResponseMaxBytes()
	resp := &http.Response{
		ContentLength: -1,
		Body:          io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{'x'}, int(limit)+1))),
	}
	body, err := readVideoProxyTaskResponse(resp)
	require.ErrorIs(t, err, errVideoProxyTaskResponseTooLarge)
	assert.Nil(t, body)
}

func TestReadVideoProxyTaskResponsePropagatesReadError(t *testing.T) {
	readErr := errors.New("read failed")
	resp := &http.Response{
		ContentLength: -1,
		Body:          io.NopCloser(errorReader{err: readErr}),
	}
	_, err := readVideoProxyTaskResponse(resp)
	require.Error(t, err)
	assert.ErrorIs(t, err, readErr)
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
