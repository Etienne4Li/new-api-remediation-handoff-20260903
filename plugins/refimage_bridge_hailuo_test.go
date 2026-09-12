package plugins_test

import (
	"bytes"
	"context"
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
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reference-image bridge pre-validates a request by handing the pinned
// plugin decoder a copy in which every uploaded file has already become a
// hosted URL. These tests run that check against the *real* hailuo decoder —
// the implementation the lietio-video endpoint plugin is based on — instead of
// a synthetic stand-in, so the file.field contract cannot drift away from the
// bridge unnoticed.

const refImageBridgeTestToken = "refimage-bridge-test-token"

func refImageBridgePNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, source))
	return buffer.Bytes()
}

// registerHailuoAsBridgePlugin loads the shipped hailuo plugin source and
// registers it under the task plugin key the bridge serves, because the bridge
// is gated on that key rather than on the manifest name.
func registerHailuoAsBridgePlugin(t *testing.T) *pluginruntime.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("hailuo")
	require.NoError(t, err)
	require.Contains(t, source, `key: "hailuo"`, "the hailuo manifest key moved; update this fixture")
	renamed := strings.Replace(source, `key: "hailuo"`, `key: "`+system_setting.RefImageUploadPluginKey+`"`, 1)
	plugin, err := pluginruntime.NewRegistry().RegisterFactory(renamed, pluginruntime.Options{Key: system_setting.RefImageUploadPluginKey})
	require.NoError(t, err)
	return plugin
}

// enableRefImageBridge points the bridge at a local host for one test.
func enableRefImageBridge(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv(system_setting.RefImageUploadEnabledEnv, "true")
	t.Setenv(system_setting.RefImageUploadEndpointEnv, endpoint)
	t.Setenv(system_setting.RefImageUploadTokenEnv, refImageBridgeTestToken)
	system_setting.ResetRefImageUploadConfigCache()
	t.Cleanup(system_setting.ResetRefImageUploadConfigCache)
}

// hailuoBridgeContext builds the request the endpoint middleware hands to the
// bridge: a parsed multipart form plus the pinned protocol view of it.
func hailuoBridgeContext(
	t *testing.T,
	model string,
	textFields map[string]string,
	fileField string,
	fileBytes []byte,
	plugin *pluginruntime.LoadedPlugin,
) *gin.Context {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range textFields {
		require.NoError(t, writer.WriteField(name, value))
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+fileField+`"; filename="frame.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(fileBytes)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	fields := map[string][]string{}
	for name, value := range textFields {
		fields[name] = []string{value}
	}
	files := []map[string]any{{
		"ref":      "request_file:" + fileField,
		"field":    fileField,
		"filename": "frame.png",
		"mimeType": "image/png",
		"size":     len(fileBytes),
	}}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{
			Path:   "/v1/videos",
			Method: http.MethodPost,
			Body:   map[string]any{"kind": string(pluginruntime.BodyMultipart), "fields": fields, "files": files},
			Files:  files,
		},
		Protocol:  "openai_video",
		Operation: "create",
		Model:     model,
	})
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{
		Plugin:    plugin,
		Protocol:  "openai_video",
		Operation: pluginruntime.HostProtocolOperation{Name: "create"},
		Model:     model,
	})
	return c
}

func bridgeProtocolFields(c *gin.Context) map[string][]string {
	value, _ := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocol := value.(pluginruntime.ProtocolRequestContext)
	body := protocol.Body.(map[string]any)
	fields, _ := body["fields"].(map[string][]string)
	return fields
}

func bridgeProtocolFiles(c *gin.Context) any {
	value, _ := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocol := value.(pluginruntime.ProtocolRequestContext)
	body := protocol.Body.(map[string]any)
	return body["files"]
}

func bridgeProtocolFileList(c *gin.Context) []map[string]any {
	value, _ := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocol := value.(pluginruntime.ProtocolRequestContext)
	return protocol.Files
}

// TestRefImageBridgeHailuoInputReferenceSurvivesUpload is the regression test
// for the hailuo `input_reference` file shape.
//
// The hailuo decoder only recognises an image through the field the client sent
// it in (`input_reference`) or through `metadata`. A probe that only populated
// the plugin-side alias (`first_image`) therefore described a request that had
// silently lost its image: the decode succeeded, but as text-to-video, and the
// uploaded URL never reached the upstream. The probe must describe the exact
// request the decoder will see after the upload, so the hosted URL is published
// under the original multipart field name as well.
func TestRefImageBridgeHailuoInputReferenceSurvivesUpload(t *testing.T) {
	uploads := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		require.Equal(t, refImageBridgeTestToken, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableRefImageBridge(t, host.URL+"/api/upload")

	plugin := registerHailuoAsBridgePlugin(t)
	c := hailuoBridgeContext(t, "MiniMax-H3", map[string]string{"model": "MiniMax-H3", "prompt": "a kite"}, "input_reference", refImageBridgePNG(t), plugin)

	verifier := service.NewTaskPluginPlaceholderVerifierForPlugin(c, plugin)
	require.NotNil(t, verifier, "the hailuo decoder must be reachable for pre-validation")

	var probeFields map[string][]string
	capturingVerifier := func(fields map[string][]string) error {
		probeFields = fields
		return verifier(fields)
	}

	require.NoError(t, service.BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, capturingVerifier))
	assert.Equal(t, 1, uploads, "a request the decoder accepts must be uploaded exactly once")

	const hostedURL = "https://z.lietio.com/bridged.png"
	fields := bridgeProtocolFields(c)

	// The probe and the rewritten request must agree on every field name: the
	// upload only replaces placeholder values with hosted URLs.
	require.NotEmpty(t, probeFields, "the bridge must pre-validate before uploading")
	assert.Equal(t, sortedKeys(fields), sortedKeys(probeFields), "probe and post-upload field sets must match")
	assert.Equal(t, []string{hostedURL}, fields["first_image"])
	assert.Equal(t, []string{hostedURL}, fields["input_reference"], "the URL must also live under the field the client sent the file in")
	assert.Contains(t, probeFields["input_reference"][0], "__refimage_pending__")
	assert.NotContains(t, strings.Join(fields["input_reference"], ","), "__refimage_pending__", "no placeholder may survive into the real request")

	// The raw file parts are gone from both views of the request.
	assert.Equal(t, []map[string]any{}, bridgeProtocolFiles(c))
	assert.Empty(t, bridgeProtocolFileList(c))

	// The rebuild path still works: the raw body was not consumed.
	form, err := common.ParseMultipartFormReusable(c)
	require.NoError(t, err)
	defer form.RemoveAll()
	assert.Len(t, form.File["input_reference"], 1)
}

// TestRefImageBridgeHailuoPostUploadDecodeKeepsReference proves the decoded
// request actually carries the reference image after the upload, i.e. the
// upstream is asked for image-to-video rather than silently dropping the
// reference.
func TestRefImageBridgeHailuoPostUploadDecodeKeepsReference(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/bridged.png"}]}`))
	}))
	defer host.Close()
	enableRefImageBridge(t, host.URL+"/api/upload")

	plugin := registerHailuoAsBridgePlugin(t)
	c := hailuoBridgeContext(t, "MiniMax-H3", map[string]string{"model": "MiniMax-H3", "prompt": "a kite"}, "input_reference", refImageBridgePNG(t), plugin)

	verifier := service.NewTaskPluginPlaceholderVerifierForPlugin(c, plugin)
	require.NotNil(t, verifier)
	require.NoError(t, service.BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, verifier))

	value, _ := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocol := value.(pluginruntime.ProtocolRequestContext)
	resolved, err := plugin.Engine.CallPath(context.Background(), "protocols", []string{"openai_video", "decodeRequest"}, protocol.JSValue())
	require.NoError(t, err, "the request the bridge produced must decode")
	decoded, ok := resolved.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "submit", decoded["kind"])
	assert.Equal(t, "image_to_video", decoded["action"], "the uploaded reference must still select image-to-video")

	requestBody, ok := decoded["requestBody"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://z.lietio.com/bridged.png", requestBody["input_reference"])
	metadata, ok := requestBody["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://z.lietio.com/bridged.png", metadata["first_frame_image"])
}

func sortedKeys(fields map[string][]string) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
