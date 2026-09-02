package router

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupMidjourneyImageRouteTestDB(t *testing.T) {
	t.Helper()
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Midjourney{}))
}

func TestMidjourneyImageRequiresAuthenticationAndOwner(t *testing.T) {
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

	owner := &model.User{Username: "mj-image-owner", AffCode: "mj-image-owner-aff", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	other := &model.User{Username: "mj-image-other", AffCode: "mj-image-other-aff", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(owner).Error)
	require.NoError(t, model.DB.Create(other).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         owner.Id,
		Key:            "mjimageownerkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         other.Id,
		Key:            "mjimageotherkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	upstreamRequests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	}))
	defer upstream.Close()

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     901,
		Key:    "channel-key",
		Status: common.ChannelStatusEnabled,
		Name:   "mj-image-test-channel",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId:    owner.Id,
		MjId:      "mj-owner-task",
		ImageUrl:  upstream.URL,
		ChannelId: 901,
		Status:    "SUCCESS",
		Progress:  "100%",
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	request := func(path, token string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	unauthenticated := request("/mj/image/mj-owner-task", "")
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	assert.NotContains(t, unauthenticated.Body.String(), "PNG")
	assert.Zero(t, upstreamRequests, "authentication must happen before fetching upstream media")

	crossUser := request("/mj/image/mj-owner-task", "mjimageotherkey")
	require.Equal(t, http.StatusNotFound, crossUser.Code)
	assert.Contains(t, crossUser.Body.String(), "midjourney_task_not_found")
	assert.Zero(t, upstreamRequests, "a task owned by another user must not be fetched")

	unknown := request("/mj/image/mj-unknown-task", "mjimageownerkey")
	require.Equal(t, http.StatusNotFound, unknown.Code)
	assert.Equal(t, crossUser.Body.String(), unknown.Body.String(), "unknown and cross-user ids must be indistinguishable")
	assert.Zero(t, upstreamRequests)

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     904,
		Key:    "second-channel-key",
		Status: common.ChannelStatusEnabled,
		Name:   "mj-image-second-channel",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId:    owner.Id,
		MjId:      "mj-owner-task",
		ImageUrl:  upstream.URL + "/second-channel",
		ChannelId: 904,
		Status:    "SUCCESS",
		Progress:  "100%",
	}).Error)
	ambiguous := request("/mj/image/mj-owner-task", "mjimageownerkey")
	require.Equal(t, http.StatusNotFound, ambiguous.Code)
	assert.Equal(t, unknown.Body.String(), ambiguous.Body.String(), "ambiguous and unknown ids must be indistinguishable")
	assert.Zero(t, upstreamRequests, "an ambiguous provider id must not select an arbitrary channel")
}

func TestMidjourneyImageOwnerGetsSafePrivateResponse(t *testing.T) {
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

	owner := &model.User{Username: "mj-image-safe-owner", AffCode: "mj-image-safe-aff", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(owner).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         owner.Id,
		Key:            "mjimagesafekey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 1, 2, 3}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("signature") != "private-media-signature" {
			http.Error(w, "missing signature", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/png; charset=binary")
		w.Header().Set("Set-Cookie", "provider-secret=should-not-forward")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(png)
	}))
	defer upstream.Close()

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     902,
		Key:    "channel-key",
		Status: common.ChannelStatusEnabled,
		Name:   "mj-image-safe-channel",
	}).Error)
	const taskID = "mj/safe/task"
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId:    owner.Id,
		MjId:      taskID,
		ImageUrl:  upstream.URL + "?signature=private-media-signature",
		ChannelId: 902,
		Status:    "SUCCESS",
		Progress:  "100%",
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, service.BuildMidjourneyImageProxyURL("", taskID), nil)
	request.Header.Set("Authorization", "Bearer mjimagesafekey")
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, bytes.Equal(png, recorder.Body.Bytes()))
	assert.Equal(t, "image/png", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store, max-age=0", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", recorder.Header().Get("Referrer-Policy"))
	assert.Equal(t, `inline; filename="midjourney-image"`, recorder.Header().Get("Content-Disposition"))
	assert.Empty(t, recorder.Header().Get("Set-Cookie"))
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))

	adminEngine := gin.New()
	adminEngine.GET("/mj/image/:id", func(c *gin.Context) {
		c.Set("id", owner.Id+1)
		c.Set("role", common.RoleAdminUser)
	}, relay.RelayMidjourneyImage)
	adminRecorder := httptest.NewRecorder()
	adminRequest := httptest.NewRequest(http.MethodGet, service.BuildMidjourneyImageProxyURL("", taskID), nil)
	adminEngine.ServeHTTP(adminRecorder, adminRequest)
	require.Equal(t, http.StatusOK, adminRecorder.Code, adminRecorder.Body.String())
	assert.True(t, bytes.Equal(png, adminRecorder.Body.Bytes()))

	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId:    owner.Id + 2,
		MjId:      taskID,
		ImageUrl:  upstream.URL + "?signature=private-media-signature",
		ChannelId: 902,
		Status:    "SUCCESS",
		Progress:  "100%",
	}).Error)
	ambiguousRecorder := httptest.NewRecorder()
	ambiguousRequest := httptest.NewRequest(http.MethodGet, service.BuildMidjourneyImageProxyURL("", taskID), nil)
	adminEngine.ServeHTTP(ambiguousRecorder, ambiguousRequest)
	require.Equal(t, http.StatusNotFound, ambiguousRecorder.Code)
	assert.Contains(t, ambiguousRecorder.Body.String(), "midjourney_task_not_found")
}

func TestMidjourneyImageRejectsNonImageUpstreamBody(t *testing.T) {
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

	owner := &model.User{Username: "mj-image-html-owner", AffCode: "mj-image-html-aff", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(owner).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         owner.Id,
		Key:            "mjimagehtmlkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer upstream.Close()
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     903,
		Key:    "channel-key",
		Status: common.ChannelStatusEnabled,
		Name:   "mj-image-html-channel",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId:    owner.Id,
		MjId:      "mj-html-task",
		ImageUrl:  upstream.URL,
		ChannelId: 903,
		Status:    "SUCCESS",
		Progress:  "100%",
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/mj/image/mj-html-task", nil)
	request.Header.Set("Authorization", "Bearer mjimagehtmlkey")
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnsupportedMediaType, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "unsupported_image_type")
	assert.NotContains(t, recorder.Body.String(), "not an image")
}
