package jsplugin

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adaptorUploadPluginKey is the plugin key the bridge serves.
const adaptorUploadPluginKey = system_setting.RefImageUploadPluginKey

const adaptorUploadToken = "adaptor-upload-token"

// adaptorRefImagePNG is a real encoded PNG: the bridge decides the media type
// from the bytes, so an arbitrary payload would be rejected before upload.
func adaptorRefImagePNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, source))
	return buffer.Bytes()
}

// adaptorUploadPluginSource rejects raw file parts, exactly like the production
// lietio-video decoder, and only accepts a bridged HTTPS URL. It also rejects a
// request whose first_image is absent, so a pre-validation probe that skipped
// the field would fail rather than silently pass.
const adaptorUploadPluginSource = `
export const meta = {apiVersion:1,key:"lietio-video",name:"Lietio Video",version:"1.0.0",author:{name:"Test"},models:["Seedance-2.0"],fetchMode:"per_task",protocols:["openai_video"]};
export const protocols = {openai_video:{
  decodeRequest: function(ctx){
    const files = (ctx.body && ctx.body.files) || [];
    if (files.length) throw new Error("file upload is not accepted on this endpoint");
    const first = ((ctx.body && ctx.body.fields && ctx.body.fields.first_image) || [])[0];
    if (!first || first.indexOf("https://z.lietio.com/") !== 0) throw new Error("first_image was not bridged");
    return {kind:"submit", model: ctx.model, action:"image_to_video", requestBody:{prompt:"p", first_image:first}};
  },
  render: function(){ return {}; }
}};
export function buildSubmitRequest(ctx){ return {url:ctx.baseUrl+"/submit",method:"POST",body:ctx.requestBody}; }
export function parseSubmitResponse(){ return {taskId:"one"}; }
export function buildQueryRequest(){ return {url:"https://provider.example"}; }
export function parseTaskResult(){ return {status:"SUCCESS"}; }
export function listArtifacts(){ return []; }
export function buildContentRequest(){ throw new Error("artifact_not_found"); }
`

// adaptorRejectingPluginSource refuses to decode anything, so the bridge must
// perform zero uploads for it.
const adaptorRejectingPluginSource = `
export const meta = {apiVersion:1,key:"lietio-video",name:"Lietio Video",version:"1.0.0",author:{name:"Test"},models:["Seedance-2.0"],fetchMode:"per_task",protocols:["openai_video"]};
export const protocols = {openai_video:{
  decodeRequest: function(){ throw new Error("unknown model parameter: seed"); },
  render: function(){ return {}; }
}};
export function buildSubmitRequest(){ return {url:"https://provider.example/submit"}; }
export function parseSubmitResponse(){ return {taskId:"one"}; }
export function buildQueryRequest(){ return {url:"https://provider.example"}; }
export function parseTaskResult(){ return {status:"SUCCESS"}; }
export function listArtifacts(){ return []; }
export function buildContentRequest(){ throw new Error("artifact_not_found"); }
`

func enableAdaptorRefImageUpload(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv(system_setting.RefImageUploadEnabledEnv, "true")
	t.Setenv(system_setting.RefImageUploadEndpointEnv, endpoint)
	t.Setenv(system_setting.RefImageUploadTokenEnv, adaptorUploadToken)
	system_setting.ResetRefImageUploadConfigCache()
	t.Cleanup(system_setting.ResetRefImageUploadConfigCache)
}

func adaptorUploadContext(t *testing.T, plugin *pluginruntime.LoadedPlugin) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "Seedance-2.0"))
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="first_image"; filename="frame.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(adaptorRefImagePNG(t))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{
		Plugin: plugin, Protocol: "openai_video", Model: "Seedance-2.0",
	})
	c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{
			Body: map[string]any{
				"kind":   string(pluginruntime.BodyMultipart),
				"fields": map[string][]string{"model": {"Seedance-2.0"}},
				"files":  []map[string]any{{"ref": "request_file:first_image", "field": "first_image"}},
			},
			Files: []map[string]any{{"ref": "request_file:first_image", "field": "first_image"}},
		},
		Protocol: "openai_video", Operation: "create", Model: "Seedance-2.0",
	})
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
		OriginModelName: "Seedance-2.0",
	}
	return c, info
}

// TestTaskAdaptorBridgesRefImageUploadBeforeValidate covers the required
// hook: the bridge runs inside ValidateRequestAndSetAction before the plugin
// decoder and before price calculation, so the decoder accepts the request.
func TestTaskAdaptorBridgesRefImageUploadBeforeValidate(t *testing.T) {
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		require.Equal(t, adaptorUploadToken, r.Header.Get("Authorization"))
		require.Equal(t, system_setting.RefImageUploadDeletesAt, r.Header.Get("x-zipline-deletes-at"))
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	plugin, err := pluginruntime.NewRegistry().Register(adaptorUploadPluginSource, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.Nil(t, taskErr, "a successfully bridged request must validate")
	assert.Equal(t, 1, uploads)
	assert.Equal(t, "image_to_video", info.Action)
	request, exists := c.Get("task_request")
	require.True(t, exists)
	assert.Equal(t, "https://z.lietio.com/bridged.png", request.(map[string]any)["first_image"])
}

// TestTaskAdaptorBuildsJSONSubmitBodyAfterBridge covers the submit stage of a
// bridged request: the plugin body is plain JSON carrying the hosted URL and no
// file placeholder, so it must reach the upstream unchanged. Before 2026-09-12
// the JSON path refused every bridged request outright ("file placeholders are
// not available after reference images were bridged"), which failed all real
// lietio-video submissions after a successful upload.
func TestTaskAdaptorBuildsJSONSubmitBodyAfterBridge(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	plugin, err := pluginruntime.NewRegistry().Register(adaptorUploadPluginSource, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	adaptor.Init(info)
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	require.True(t, service.RefImageUploadBridged(c))

	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(raw, &decoded))
	assert.Equal(t, "p", decoded["prompt"])
	assert.Equal(t, "https://z.lietio.com/bridged.png", decoded["first_image"])
}

// TestTaskAdaptorRefusesFilePlaceholderAfterBridge keeps the guard for the one
// case it is meant for: a plugin body that still asks for the raw file bytes
// after the bridge replaced them with a hosted URL.
func TestTaskAdaptorRefusesFilePlaceholderAfterBridge(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	source := strings.Replace(
		adaptorUploadPluginSource,
		`export function buildSubmitRequest(ctx){ return {url:ctx.baseUrl+"/submit",method:"POST",body:ctx.requestBody}; }`,
		`export function buildSubmitRequest(ctx){ return {url:ctx.baseUrl+"/submit",method:"POST",body:{prompt:"p",image:{__fileRef:"request_file:first_image",encoding:"base64"}}}; }`,
		1,
	)
	require.NotEqual(t, adaptorUploadPluginSource, source)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	adaptor.Init(info)
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

	_, err = adaptor.BuildRequestBody(c, info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available after reference images were bridged")
}

// TestTaskAdaptorDoesNotUploadForRejectedRequest proves the adaptor also obeys
// validation-first ordering: a request the decoder rejects costs zero uploads.
func TestTaskAdaptorDoesNotUploadForRejectedRequest(t *testing.T) {
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	plugin, err := pluginruntime.NewRegistry().Register(adaptorRejectingPluginSource, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, "refimage_upload_failed", taskErr.Code)
	assert.Zero(t, uploads, "a request the decoder rejects must never be uploaded")
}

// TestTaskAdaptorRefImageUploadFailureDoesNotReachPluginOrBilling covers the
// "no billing, no upstream" requirement at the adaptor boundary: a failed
// upload returns a neutral error before any request is built or sent, and the
// caller sees no host detail.
func TestTaskAdaptorRefImageUploadFailureDoesNotReachPluginOrBilling(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"Invalid token"}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	plugin, err := pluginruntime.NewRegistry().Register(adaptorUploadPluginSource, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, "refimage_upload_failed", taskErr.Code)
	assert.Contains(t, taskErr.Message, service.RefImageUploadNeutralMessage)
	assert.NotContains(t, taskErr.Message, "Invalid token")

	// No submit descriptor was built, so BuildRequestURL/BuildRequestBody cannot
	// produce an upstream request.
	_, urlErr := adaptor.BuildRequestURL(info)
	assert.Error(t, urlErr, "the upstream request must never be built after a failed upload")
}

// TestTaskAdaptorRefusesWithoutModelAccessBeforeUploading proves the adaptor
// call site also settles token model access before any byte reaches the image
// host: a token restricted to another model costs zero uploads and is refused
// with the distributor's rule rather than reaching the bridge.
func TestTaskAdaptorRefusesWithoutModelAccessBeforeUploading(t *testing.T) {
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	plugin, err := pluginruntime.NewRegistry().Register(adaptorUploadPluginSource, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	c.Set(string(constant.ContextKeyTokenModelLimitEnabled), true)
	c.Set(string(constant.ContextKeyTokenModelLimit), map[string]bool{"some-other-model": true})
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, "refimage_model_forbidden", taskErr.Code)
	assert.Equal(t, http.StatusForbidden, taskErr.StatusCode)
	assert.Zero(t, uploads, "a token without model access must never reach the image host")
}

// TestTaskAdaptorAllowsWhenModelLimitPermitsModel proves the adaptor gate does
// not interfere when the token allowlist authorizes this model.
func TestTaskAdaptorAllowsWhenModelLimitPermitsModel(t *testing.T) {
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	plugin, err := pluginruntime.NewRegistry().Register(adaptorUploadPluginSource, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	c.Set(string(constant.ContextKeyTokenModelLimitEnabled), true)
	c.Set(string(constant.ContextKeyTokenModelLimit), map[string]bool{"Seedance-2.0": true})
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.Nil(t, taskErr)
	assert.Equal(t, 1, uploads, "an authorized model must still be bridged")
}

// TestTaskAdaptorSkipsRefImageUploadForOtherPlugins covers the plugin gate at
// the adaptor boundary.
func TestTaskAdaptorSkipsRefImageUploadForOtherPlugins(t *testing.T) {
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableAdaptorRefImageUpload(t, host.URL+"/api/upload")

	source := `
export const meta = {apiVersion:1,key:"other-plugin",name:"Other",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task",protocols:["openai_video"]};
export const protocols = {openai_video:{
  decodeRequest: function(ctx){
    const files = (ctx.body && ctx.body.files) || [];
    if (!files.length) throw new Error("raw parts must be preserved for other plugins");
    return {kind:"submit", model: ctx.model, requestBody:{prompt:"p"}};
  },
  render: function(){ return {}; }
}};
export function buildSubmitRequest(ctx){ return {url:ctx.baseUrl+"/submit",body:ctx.requestBody}; }
export function parseSubmitResponse(){ return {taskId:"one"}; }
export function buildQueryRequest(){ return {url:"https://provider.example"}; }
export function parseTaskResult(){ return {status:"SUCCESS"}; }
export function listArtifacts(){ return []; }
export function buildContentRequest(){ throw new Error("artifact_not_found"); }
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	c, info := adaptorUploadContext(t, plugin)
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{
		Plugin: plugin, Protocol: "openai_video", Model: "Seedance-2.0",
	})
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.Nil(t, taskErr)
	assert.Zero(t, uploads, "only lietio-video may trigger an upload")
}
