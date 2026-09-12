package middleware

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const middlewareUploadToken = "middleware-upload-token"

// middlewareRefImagePNG is a real encoded PNG: the bridge decides the media
// type from the bytes, so a placeholder payload would be rejected outright.
func middlewareRefImagePNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, source))
	return buffer.Bytes()
}

// uploadAcceptingDecodeRequest mimics the lietio-video plugin contract: URL
// fields are accepted, raw file parts are rejected, and the rewrite is visible
// as first_image. It also records that a pre-validation probe ran, by rejecting
// requests whose first_image is missing.
const uploadAcceptingDecodeRequest = `
if (ctx.body.kind !== "multipart") throw new Error("expected multipart");
const files = ctx.body.files || [];
if (files.length) throw new Error("file upload is not accepted on this endpoint");
const first = (ctx.body.fields.first_image || [])[0];
if (!first || first.indexOf("https://z.lietio.com/") !== 0) throw new Error("first_image was not bridged");
return {model: ctx.model, action: "image_to_video", requestBody: {prompt: "p", first_image: first}};
`

// uploadRejectingDecodeRequest rejects every request, so the bridge must refuse
// to upload anything.
const uploadRejectingDecodeRequest = `
throw new Error("unknown model parameter: seed");
`

func enableMiddlewareRefImageUpload(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv(system_setting.RefImageUploadEnabledEnv, "true")
	t.Setenv(system_setting.RefImageUploadEndpointEnv, endpoint)
	t.Setenv(system_setting.RefImageUploadTokenEnv, middlewareUploadToken)
	system_setting.ResetRefImageUploadConfigCache()
	t.Cleanup(system_setting.ResetRefImageUploadConfigCache)
}

func videoMultipartRequest(t *testing.T, fields map[string]string, files [][4]string) (*http.Request, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		require.NoError(t, writer.WriteField(name, value))
	}
	for _, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+file[0]+`"; filename="`+file[1]+`"`)
		header.Set("Content-Type", file[2])
		part, err := writer.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write([]byte(file[3]))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request, writer.FormDataContentType()
}

// TestPrepareTaskPluginEndpointBridgesRefImageUploadBeforeDecode proves the
// bridge runs ahead of the pinned protocol decoder: the plugin, which rejects
// raw file parts, still sees a rewritten HTTPS URL and the chain reaches
// distribution.
func TestPrepareTaskPluginEndpointBridgesRefImageUploadBeforeDecode(t *testing.T) {
	const key = system_setting.RefImageUploadPluginKey
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		require.Equal(t, middlewareUploadToken, r.Header.Get("Authorization"))
		require.Equal(t, system_setting.RefImageUploadDeletesAt, r.Header.Get("x-zipline-deletes-at"))
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.jpg"}]}`))
	}))
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")

	_, err := pluginruntime.DefaultRegistry.Register(taskProtocolPluginSource(
		key,
		"1.0.0",
		`["Seedance-2.0"]`,
		"/v1/videos",
		uploadAcceptingDecodeRequest,
	), pluginruntime.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pluginruntime.DefaultRegistry.Unregister(key)) })

	reachedDistribution := false
	router := gin.New()
	router.POST(
		"/v1/videos",
		PinTaskPluginEndpoint(),
		PrepareTaskPluginEndpoint(),
		func(c *gin.Context) {
			reachedDistribution = true
			c.Status(http.StatusNoContent)
		},
	)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "Seedance-2.0", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.True(t, reachedDistribution)
	assert.Equal(t, 1, uploads)
}

// TestPrepareTaskPluginEndpointValidatesBeforeUploading proves the ordering the
// bridge depends on: a request the plugin decoder rejects costs zero uploads,
// so an invalid model parameter can never reach the image host.
func TestPrepareTaskPluginEndpointValidatesBeforeUploading(t *testing.T) {
	const key = system_setting.RefImageUploadPluginKey
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.jpg"}]}`))
	}))
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")

	_, err := pluginruntime.DefaultRegistry.Register(taskProtocolPluginSource(
		key,
		"1.0.0",
		`["Seedance-2.0"]`,
		"/v1/videos",
		uploadRejectingDecodeRequest,
	), pluginruntime.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pluginruntime.DefaultRegistry.Unregister(key)) })

	reachedDistribution := false
	router := gin.New()
	router.POST(
		"/v1/videos",
		PinTaskPluginEndpoint(),
		PrepareTaskPluginEndpoint(),
		func(c *gin.Context) {
			reachedDistribution = true
			c.Status(http.StatusNoContent)
		},
	)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "Seedance-2.0", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assert.False(t, reachedDistribution)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Zero(t, uploads, "a request the decoder rejects must never be uploaded")
	assert.Contains(t, recorder.Body.String(), service.RefImageUploadRejectedPrefix)
	assert.Contains(t, recorder.Body.String(), "unknown model parameter: seed")
}

// TestPrepareTaskPluginEndpointUploadFailureStopsBeforeDistribution proves a
// failed upload aborts the request ahead of distribution. Distribution is what
// leads to RelayTaskSubmit, price calculation, pre-consumption, and the
// upstream call, so nothing is billed and no upstream request is made.
func TestPrepareTaskPluginEndpointUploadFailureStopsBeforeDistribution(t *testing.T) {
	const key = system_setting.RefImageUploadPluginKey
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal-host-detail-must-not-leak"))
	}))
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")

	_, err := pluginruntime.DefaultRegistry.Register(taskProtocolPluginSource(
		key,
		"1.0.0",
		`["Seedance-2.0"]`,
		"/v1/videos",
		uploadAcceptingDecodeRequest,
	), pluginruntime.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pluginruntime.DefaultRegistry.Unregister(key)) })

	reachedDistribution := false
	router := gin.New()
	router.POST(
		"/v1/videos",
		PinTaskPluginEndpoint(),
		PrepareTaskPluginEndpoint(),
		func(c *gin.Context) {
			reachedDistribution = true
			c.Status(http.StatusNoContent)
		},
	)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "Seedance-2.0", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assert.False(t, reachedDistribution, "a failed upload must not reach distribution, billing, or the upstream")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), service.RefImageUploadNeutralMessage)
	assert.NotContains(t, recorder.Body.String(), "internal-host-detail-must-not-leak")
}

// TestPrepareTaskPluginEndpointKeepsJsonBehaviourUnchanged proves the bridge
// leaves the JSON path alone: a JSON submit triggers no upload and still
// reaches the plugin decoder and distribution exactly as before.
func TestPrepareTaskPluginEndpointKeepsJsonBehaviourUnchanged(t *testing.T) {
	const key = system_setting.RefImageUploadPluginKey
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.jpg"}]}`))
	}))
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")

	_, err := pluginruntime.DefaultRegistry.Register(taskProtocolPluginSource(
		key,
		"1.0.0",
		`["Seedance-2.0"]`,
		"/v1/videos",
		`if (ctx.body.kind !== "json") throw new Error("expected a json body");
		 return {model: ctx.model, action: "text_to_video", requestBody: {prompt: "p"}};`,
	), pluginruntime.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pluginruntime.DefaultRegistry.Unregister(key)) })

	reachedDistribution := false
	router := gin.New()
	router.POST(
		"/v1/videos",
		PinTaskPluginEndpoint(),
		PrepareTaskPluginEndpoint(),
		func(c *gin.Context) {
			reachedDistribution = true
			c.Status(http.StatusNoContent)
		},
	)
	body := `{"model":"Seedance-2.0","prompt":"p"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.True(t, reachedDistribution)
	assert.Zero(t, uploads, "a JSON request must never trigger an upload")
}

// TestPrepareTaskPluginEndpointSkipsBridgeForOtherPlugins proves the plugin gate
// end to end: a non-lietio plugin keeps the existing file-part behaviour.
func TestPrepareTaskPluginEndpointSkipsBridgeForOtherPlugins(t *testing.T) {
	const key = "other-video-plugin"
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.jpg"}]}`))
	}))
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")

	_, err := pluginruntime.DefaultRegistry.Register(taskProtocolPluginSource(
		key,
		"1.0.0",
		`["other-video-model"]`,
		"/v1/videos",
		`if (ctx.body.kind !== "multipart") throw new Error("expected multipart");
		 if (!ctx.body.files || ctx.body.files.length !== 1) throw new Error("raw file parts must be preserved");
		 return {model: ctx.model, requestBody: {prompt: "p"}};`,
	), pluginruntime.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pluginruntime.DefaultRegistry.Unregister(key)) })

	reachedDistribution := false
	router := gin.New()
	router.POST(
		"/v1/videos",
		PinTaskPluginEndpoint(),
		PrepareTaskPluginEndpoint(),
		func(c *gin.Context) {
			reachedDistribution = true
			c.Status(http.StatusNoContent)
		},
	)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "other-video-model", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.True(t, reachedDistribution)
	assert.Zero(t, uploads, "only lietio-video may trigger an upload")
}

// pinTokenModelLimit publishes the token model allowlist on the context exactly
// as middleware.TokenAuth does for a real token, so the pre-upload gate sees the
// same inputs the distributor would.
func pinTokenModelLimit(c *gin.Context, enabled bool, limit map[string]bool) {
	c.Set(string(constant.ContextKeyTokenModelLimitEnabled), enabled)
	if limit != nil {
		c.Set(string(constant.ContextKeyTokenModelLimit), limit)
	}
}

// refImageUploadAuthorizationRouter wires the real endpoint chain behind a
// handler that injects the token model allowlist, mirroring TokenAuth.
func refImageUploadAuthorizationRouter(t *testing.T, limit func(*gin.Context), reached *bool) *gin.Engine {
	t.Helper()
	router := gin.New()
	router.POST(
		"/v1/videos",
		limit,
		PinTaskPluginEndpoint(),
		PrepareTaskPluginEndpoint(),
		func(c *gin.Context) {
			*reached = true
			c.Status(http.StatusNoContent)
		},
	)
	return router
}

// uploadCountingHost records how many times the image host was contacted.
func uploadCountingHost(t *testing.T, uploads *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.jpg"}]}`))
	}))
}

func registerUploadingEndpointPlugin(t *testing.T) {
	t.Helper()
	_, err := pluginruntime.DefaultRegistry.Register(taskProtocolPluginSource(
		system_setting.RefImageUploadPluginKey,
		"1.0.0",
		`["Seedance-2.0"]`,
		"/v1/videos",
		uploadAcceptingDecodeRequest,
	), pluginruntime.Options{})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, pluginruntime.DefaultRegistry.Unregister(system_setting.RefImageUploadPluginKey))
	})
}

// TestPrepareTaskPluginEndpointUploadsZeroTimesWithoutModelAccess is the
// regression test for the wasted-upload path: a token with a valid credential
// but no access to the requested model must be refused *before* the image host
// is contacted, and the refusal must come from the same rule the distributor
// applies rather than a bypass.
func TestPrepareTaskPluginEndpointUploadsZeroTimesWithoutModelAccess(t *testing.T) {
	require.NoError(t, i18n.Init())
	uploads := 0
	host := uploadCountingHost(t, &uploads)
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")
	registerUploadingEndpointPlugin(t)

	reachedDistribution := false
	router := refImageUploadAuthorizationRouter(t, func(c *gin.Context) {
		pinTokenModelLimit(c, true, map[string]bool{"some-other-model": true})
	}, &reachedDistribution)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "Seedance-2.0", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "Seedance-2.0", "the refusal must name the unauthorized model")
	assert.False(t, reachedDistribution, "an unauthorized request must not reach distribution")
	assert.Zero(t, uploads, "a token without model access must cost zero image-host uploads")
}

// TestPrepareTaskPluginEndpointUploadsZeroTimesWithEmptyModelLimit covers the
// other denying shape: the allowlist is enabled but empty, which authorizes no
// model at all. It must behave exactly like the distributor's "no model access"
// branch, again with zero uploads.
func TestPrepareTaskPluginEndpointUploadsZeroTimesWithEmptyModelLimit(t *testing.T) {
	require.NoError(t, i18n.Init())
	uploads := 0
	host := uploadCountingHost(t, &uploads)
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")
	registerUploadingEndpointPlugin(t)

	reachedDistribution := false
	router := refImageUploadAuthorizationRouter(t, func(c *gin.Context) {
		pinTokenModelLimit(c, true, nil)
	}, &reachedDistribution)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "Seedance-2.0", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	assert.False(t, reachedDistribution)
	assert.Zero(t, uploads, "an empty allowlist must cost zero image-host uploads")
}

// TestPrepareTaskPluginEndpointUploadsWhenModelLimitAllowsModel proves the gate
// is not a blanket block: a token restricted to exactly this model still gets
// the reference images bridged as before.
func TestPrepareTaskPluginEndpointUploadsWhenModelLimitAllowsModel(t *testing.T) {
	require.NoError(t, i18n.Init())
	uploads := 0
	host := uploadCountingHost(t, &uploads)
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")
	registerUploadingEndpointPlugin(t)

	reachedDistribution := false
	router := refImageUploadAuthorizationRouter(t, func(c *gin.Context) {
		pinTokenModelLimit(c, true, map[string]bool{"Seedance-2.0": true})
	}, &reachedDistribution)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "Seedance-2.0", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.True(t, reachedDistribution)
	assert.Equal(t, 1, uploads, "an allowed model must still be bridged")
}

// TestPrepareTaskPluginEndpointUploadsWhenTokenHasNoModelLimit proves the
// default (unrestricted) token keeps working unchanged: no allowlist means every
// model is authorized, so the bridge runs exactly once.
func TestPrepareTaskPluginEndpointUploadsWhenTokenHasNoModelLimit(t *testing.T) {
	require.NoError(t, i18n.Init())
	uploads := 0
	host := uploadCountingHost(t, &uploads)
	defer host.Close()
	enableMiddlewareRefImageUpload(t, host.URL+"/api/upload")
	registerUploadingEndpointPlugin(t)

	reachedDistribution := false
	router := refImageUploadAuthorizationRouter(t, func(c *gin.Context) {
		pinTokenModelLimit(c, false, nil)
	}, &reachedDistribution)
	request, _ := videoMultipartRequest(t,
		map[string]string{"model": "Seedance-2.0", "prompt": "p"},
		[][4]string{{"first_image", "frame.png", "image/png", string(middlewareRefImagePNG(t))}},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.True(t, reachedDistribution)
	assert.Equal(t, 1, uploads, "an unrestricted token must still be bridged")
}

// TestTokenModelAuthorizationRuleIsSharedWithDistributor pins the extraction:
// the middleware helper the distributor now uses must report the same verdict as
// the shared service rule for every shape, so the pre-upload gate can never
// authorize something the distributor would refuse.
func TestTokenModelAuthorizationRuleIsSharedWithDistributor(t *testing.T) {
	cases := []struct {
		name    string
		limit   map[string]bool
		model   string
		allowed bool
	}{
		{"exact", map[string]bool{"m1": true}, "m1", true},
		{"absent", map[string]bool{"m1": true}, "m2", false},
		{"empty", map[string]bool{}, "m1", false},
		{"wildcard", map[string]bool{"gemini-2.5-flash-thinking-*": true}, "gemini-2.5-flash-thinking-8192", true},
		{"alias-modifier", map[string]bool{"claude-3-7-sonnet": true}, "claude-3-7-sonnet-thinking", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.allowed, tokenModelLimitAllows(testCase.limit, testCase.model))
			assert.Equal(t, testCase.allowed, service.TokenModelLimitAllows(testCase.limit, testCase.model))
		})
	}
}

// TestAuthorizeTokenModelAccessReportsDenialShapes covers the shared read-only
// rule directly, including the unrestricted default.
func TestAuthorizeTokenModelAccessReportsDenialShapes(t *testing.T) {
	unrestricted, _ := gin.CreateTestContext(httptest.NewRecorder())
	pinTokenModelLimit(unrestricted, false, nil)
	assert.Equal(t, service.TokenModelAccessUnrestricted, service.AuthorizeTokenModelAccess(unrestricted, "m1"))

	flat, _ := gin.CreateTestContext(httptest.NewRecorder())
	pinTokenModelLimit(flat, true, nil)
	assert.Equal(t, service.TokenModelAccessLimitEmpty, service.AuthorizeTokenModelAccess(flat, "m1"))

	allowed, _ := gin.CreateTestContext(httptest.NewRecorder())
	pinTokenModelLimit(allowed, true, map[string]bool{"m1": true})
	assert.Equal(t, service.TokenModelAccessAllowed, service.AuthorizeTokenModelAccess(allowed, "m1"))

	forbidden, _ := gin.CreateTestContext(httptest.NewRecorder())
	pinTokenModelLimit(forbidden, true, map[string]bool{"m1": true})
	assert.Equal(t, service.TokenModelAccessModelForbidden, service.AuthorizeTokenModelAccess(forbidden, "m2"))

	// The rule is read-only: it must not disturb the context it inspects.
	value, present := common.GetContextKey(forbidden, constant.ContextKeyTokenModelLimit)
	require.True(t, present)
	assert.Equal(t, map[string]bool{"m1": true}, value)
}
