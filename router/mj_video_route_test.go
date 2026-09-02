package router

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type midjourneyVideoRouteFixture struct {
	engine     *gin.Engine
	owner      *model.User
	other      *model.User
	channelID  int
	ownerToken string
	otherToken string
}

func setupMidjourneyVideoRouteFixture(t *testing.T) midjourneyVideoRouteFixture {
	t.Helper()
	setupMidjourneyImageRouteTestDB(t)
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalSSRFProtection := system_setting.GetFetchSetting().EnableSSRFProtection
	common.MemoryCacheEnabled = false
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"fetch_setting.enable_ssrf_protection": "false"}))
	service.InitHttpClient()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		_ = config.GlobalConfig.LoadFromDB(map[string]string{"fetch_setting.enable_ssrf_protection": fmt.Sprintf("%t", originalSSRFProtection)})
	})

	owner := &model.User{Username: "mj-video-owner", AffCode: "mj-video-owner-aff", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	other := &model.User{Username: "mj-video-other", AffCode: "mj-video-other-aff", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(owner).Error)
	require.NoError(t, model.DB.Create(other).Error)
	const ownerToken = "mjvideoownerkey"
	const otherToken = "mjvideootherkey"
	for _, token := range []model.Token{
		{UserId: owner.Id, Key: ownerToken, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true},
		{UserId: other.Id, Key: otherToken, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true},
	} {
		token := token
		require.NoError(t, model.DB.Create(&token).Error)
	}
	const channelID = 951
	require.NoError(t, model.DB.Create(&model.Channel{Id: channelID, Key: "channel-key", Status: common.ChannelStatusEnabled, Name: "mj-video-channel"}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	return midjourneyVideoRouteFixture{
		engine: engine, owner: owner, other: other, channelID: channelID,
		ownerToken: ownerToken, otherToken: otherToken,
	}
}

func midjourneyVideoTestBytes() []byte {
	return []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2',
		0x00, 0x00, 0x00, 0x00, 'm', 'p', '4', '2', 'i', 's', 'o', 'm',
	}
}

func requestMidjourneyVideo(engine *gin.Engine, path, token string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestMidjourneyVideoRequiresAuthenticationAndOwner(t *testing.T) {
	fixture := setupMidjourneyVideoRouteFixture(t)
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(midjourneyVideoTestBytes())
	}))
	defer upstream.Close()
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: fixture.owner.Id, MjId: "mj-video-private", VideoUrl: upstream.URL,
		ChannelId: fixture.channelID, Status: "SUCCESS", Progress: "100%",
	}).Error)

	unauthenticated := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-private", "")
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	crossUser := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-private", fixture.otherToken)
	unknown := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-unknown", fixture.ownerToken)
	require.Equal(t, http.StatusNotFound, crossUser.Code)
	require.Equal(t, http.StatusNotFound, unknown.Code)
	assert.Equal(t, unknown.Body.String(), crossUser.Body.String())
	assert.Zero(t, upstreamRequests.Load())
}

func TestMidjourneyVideoProxyURLRoundTripsTaskIDContainingSlash(t *testing.T) {
	fixture := setupMidjourneyVideoRouteFixture(t)
	video := midjourneyVideoTestBytes()
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(video)
	}))
	defer upstream.Close()

	const taskID = "mj/video/with/slash"
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: fixture.owner.Id, MjId: taskID, VideoUrl: upstream.URL,
		ChannelId: fixture.channelID, Status: "SUCCESS", Progress: "100%",
	}).Error)

	proxyURL := service.BuildMidjourneyVideoProxyURL("", taskID)
	recorder := requestMidjourneyVideo(fixture.engine, proxyURL, fixture.ownerToken)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, video, recorder.Body.Bytes())
	assert.EqualValues(t, 1, upstreamRequests.Load())
}

func TestMidjourneyVideoPreservesSignedQueryAndEmitsPrivateResponse(t *testing.T) {
	fixture := setupMidjourneyVideoRouteFixture(t)
	video := midjourneyVideoTestBytes()
	const signedQuery = "X-Amz-Signature=a%2Fb%2Bc&response-content-disposition=inline%3B+filename%3Dclip.mp4"
	var receivedQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "video/mp4; charset=binary")
		w.Header().Set("Set-Cookie", "provider-secret=do-not-forward")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Disposition", `attachment; filename="provider-secret.html"`)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(video)
	}))
	defer upstream.Close()
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: fixture.owner.Id, MjId: "mj-video-signed", VideoUrl: upstream.URL + "?" + signedQuery,
		ChannelId: fixture.channelID, Status: "SUCCESS", Progress: "100%",
	}).Error)

	recorder := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-signed", fixture.ownerToken)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, signedQuery, receivedQuery)
	assert.Equal(t, video, recorder.Body.Bytes())
	assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store, max-age=0", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, `inline; filename="midjourney-video"`, recorder.Header().Get("Content-Disposition"))
	assert.Empty(t, recorder.Header().Get("Set-Cookie"))
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
}

func TestMidjourneyIndexedVideoUsesRequestedListEntry(t *testing.T) {
	fixture := setupMidjourneyVideoRouteFixture(t)
	video := midjourneyVideoTestBytes()
	var firstRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstRequests.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(video)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "index-signature", r.URL.Query().Get("sig"))
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(video)
	}))
	defer second.Close()
	encoded, err := common.Marshal([]dto.ImgUrls{{Url: first.URL}, {Url: second.URL + "?sig=index-signature"}})
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: fixture.owner.Id, MjId: "mj-video-list", VideoUrls: string(encoded),
		ChannelId: fixture.channelID, Status: "SUCCESS", Progress: "100%",
	}).Error)

	recorder := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-list/1", fixture.ownerToken)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, video, recorder.Body.Bytes())
	assert.Zero(t, firstRequests.Load())
	outOfBounds := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-list/2", fixture.ownerToken)
	assert.Equal(t, http.StatusNotFound, outOfBounds.Code)
}

func TestMidjourneyVideoRejectsMIMEConflictAndOversizedBody(t *testing.T) {
	fixture := setupMidjourneyVideoRouteFixture(t)
	video := midjourneyVideoTestBytes()
	wrongMIME := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/webm")
		_, _ = w.Write(video)
	}))
	defer wrongMIME.Close()
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: fixture.owner.Id, MjId: "mj-video-wrong-mime", VideoUrl: wrongMIME.URL,
		ChannelId: fixture.channelID, Status: "SUCCESS", Progress: "100%",
	}).Error)

	wrongMIMERecorder := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-wrong-mime", fixture.ownerToken)
	require.Equal(t, http.StatusUnsupportedMediaType, wrongMIMERecorder.Code)
	assert.Contains(t, wrongMIMERecorder.Body.String(), "unsupported_video_type")
	assert.NotContains(t, wrongMIMERecorder.Body.String(), string(video))

	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })
	oversized := append(append([]byte(nil), video...), bytes.Repeat([]byte{'x'}, 1<<20)...)
	tooLarge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(oversized)
	}))
	defer tooLarge.Close()
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: fixture.owner.Id, MjId: "mj-video-too-large", VideoUrl: tooLarge.URL,
		ChannelId: fixture.channelID, Status: "SUCCESS", Progress: "100%",
	}).Error)

	tooLargeRecorder := requestMidjourneyVideo(fixture.engine, "/mj/video/mj-video-too-large", fixture.ownerToken)
	require.Equal(t, http.StatusBadGateway, tooLargeRecorder.Code)
	assert.Contains(t, tooLargeRecorder.Body.String(), "video_too_large")
	assert.Less(t, len(tooLargeRecorder.Body.Bytes()), 1024)
}

func TestMidjourneyVideoRejectsMalformedOrOversizedURLLists(t *testing.T) {
	fixture := setupMidjourneyVideoRouteFixture(t)
	for name, raw := range map[string]string{
		"malformed": "not-json",
		"oversized": strings.Repeat("x", service.MaxMidjourneyVideoURLsJSONBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			taskID := "mj-video-list-" + name
			require.NoError(t, model.DB.Table("midjourneys").Create(map[string]interface{}{
				"user_id": fixture.owner.Id, "mj_id": taskID, "video_urls": raw,
				"channel_id": fixture.channelID, "status": "SUCCESS", "progress": "100%",
			}).Error)
			recorder := requestMidjourneyVideo(fixture.engine, "/mj/video/"+taskID+"/0", fixture.ownerToken)
			assert.Equal(t, http.StatusNotFound, recorder.Code)
		})
	}
}
