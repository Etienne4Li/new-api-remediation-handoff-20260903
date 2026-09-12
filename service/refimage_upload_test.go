package service

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/webp"
)

const refImageUploadTestToken = "upload-token-for-tests"

// refImageUploadTestImageBytes builds a genuine encoded image so the byte
// sniffing and dimension checks see a real file rather than a label.
func refImageUploadTestImageBytes(t *testing.T, format string) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	switch format {
	case "image/png":
		require.NoError(t, png.Encode(&buffer, source))
	case "image/jpeg":
		require.NoError(t, jpeg.Encode(&buffer, source, nil))
	case "image/gif":
		require.NoError(t, gif.Encode(&buffer, source, nil))
	default:
		t.Fatalf("unsupported test image format %q", format)
	}
	return buffer.Bytes()
}

// refImageUploadTestWebPBytes is a real 1x1 lossless WebP. The standard library
// cannot encode WebP, so the fixture is embedded and its decodability is
// asserted by TestRefImageUploadWebPFixtureDecodes.
const refImageUploadTestWebPBase64 = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

func refImageUploadTestWebPBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(refImageUploadTestWebPBase64)
	require.NoError(t, err)
	return raw
}

// enableRefImageUploadConfig turns the bridge on for one test and points it at
// the given local endpoint. Tests never reach the public network.
func enableRefImageUploadConfig(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv(system_setting.RefImageUploadEnabledEnv, "true")
	t.Setenv(system_setting.RefImageUploadEndpointEnv, endpoint)
	t.Setenv(system_setting.RefImageUploadTokenEnv, refImageUploadTestToken)
	system_setting.ResetRefImageUploadConfigCache()
	t.Cleanup(system_setting.ResetRefImageUploadConfigCache)
}

// allowRefImageUpload is the permissive pre-validation used by tests that are
// not about validation themselves.
func allowRefImageUpload(map[string][]string) error { return nil }

type refImageUploadTestFile struct {
	field       string
	filename    string
	contentType string
	content     []byte
}

func refImageUploadTestContext(t *testing.T, files []refImageUploadTestFile, extraFields map[string]string) *gin.Context {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range extraFields {
		require.NoError(t, writer.WriteField(name, value))
	}
	for _, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+file.field+`"; filename="`+file.filename+`"`)
		header.Set("Content-Type", file.contentType)
		part, err := writer.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write(file.content)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	return c
}

// refImageUploadTestPNGFile is the common single-PNG fixture.
func refImageUploadTestPNGFile(t *testing.T) refImageUploadTestFile {
	t.Helper()
	return refImageUploadTestFile{
		field:       "first_image",
		filename:    "frame.png",
		contentType: "image/png",
		content:     refImageUploadTestImageBytes(t, "image/png"),
	}
}

func pinRefImageUploadProtocolContext(c *gin.Context, fields map[string][]string) {
	c.Set(pluginruntime.ContextKeyProtocolRequest, pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{
			Body:  map[string]any{"kind": string(pluginruntime.BodyMultipart), "fields": fields, "files": []map[string]any{}},
			Files: []map[string]any{{"ref": "request_file:first_image", "field": "first_image"}},
		},
		Protocol:  "openai_video",
		Operation: "create",
		Model:     "Seedance-2.0",
	})
}

func protocolFields(c *gin.Context) map[string][]string {
	value, _ := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocol := value.(pluginruntime.ProtocolRequestContext)
	body := protocol.Body.(map[string]any)
	fields, _ := body["fields"].(map[string][]string)
	return fields
}

func protocolFiles(c *gin.Context) any {
	value, _ := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocol := value.(pluginruntime.ProtocolRequestContext)
	body := protocol.Body.(map[string]any)
	return body["files"]
}

func protocolFileList(c *gin.Context) []map[string]any {
	value, _ := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocol := value.(pluginruntime.ProtocolRequestContext)
	return protocol.Files
}

// TestRefImageUploadWebPFixtureDecodes guards the embedded WebP fixture: if it
// ever stops decoding, the WebP cases below would silently stop testing WebP.
func TestRefImageUploadWebPFixtureDecodes(t *testing.T) {
	config, err := webp.DecodeConfig(bytes.NewReader(refImageUploadTestWebPBytes(t)))
	require.NoError(t, err)
	assert.Equal(t, 1, config.Width)
	assert.Equal(t, 1, config.Height)
}

// TestBridgeReferenceImageUploadRewritesFieldWithHostedURL covers the happy
// path: one png becomes one hosted HTTPS URL in the plugin field, the raw
// binary entry is dropped, and the request body stays re-readable.
func TestBridgeReferenceImageUploadRewritesFieldWithHostedURL(t *testing.T) {
	var receivedAuth, receivedExpiry, receivedContentType string
	var receivedField, receivedFilename string
	var receivedUpload []byte
	pngBytes := refImageUploadTestImageBytes(t, "image/png")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedExpiry = r.Header.Get("x-zipline-deletes-at")
		receivedContentType = r.Header.Get("Content-Type")
		require.NoError(t, r.ParseMultipartForm(1<<20))
		for name, headers := range r.MultipartForm.File {
			receivedField = name
			receivedFilename = headers[0].Filename
			opened, openErr := headers[0].Open()
			require.NoError(t, openErr)
			receivedUpload, openErr = io.ReadAll(opened)
			opened.Close()
			require.NoError(t, openErr)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"id":"1","name":"ref.png","url":"https://z.lietio.com/ref.png"},{"url":"https://z.lietio.com/extra.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{
		{field: "first_image", filename: "frame.png", contentType: "image/png", content: pngBytes},
	}, map[string]string{"prompt": "hello"})
	pinRefImageUploadProtocolContext(c, map[string][]string{"prompt": {"hello"}})

	require.NoError(t, BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload))

	assert.Equal(t, refImageUploadTestToken, receivedAuth, "the dedicated host credential must be sent as the Authorization header")
	assert.Equal(t, system_setting.RefImageUploadDeletesAt, receivedExpiry)
	assert.Contains(t, receivedContentType, "multipart/form-data")
	assert.Equal(t, "file", receivedField)
	assert.Equal(t, "frame.png", receivedFilename)
	assert.Equal(t, pngBytes, receivedUpload, "the image host must receive the real file bytes")
	assert.NotContains(t, string(receivedUpload), refImageUploadPlaceholderSegment, "the placeholder URL must never be sent to the image host")
	assert.Equal(t, []string{"https://z.lietio.com/ref.png"}, protocolFields(c)["first_image"])
	assert.Equal(t, []map[string]any{}, protocolFiles(c))
	assert.Empty(t, protocolFileList(c), "the raw file list must be cleared on every view of the request")

	// The raw request body must remain parseable so BuildRequestBody still works.
	form, err := common.ParseMultipartFormReusable(c)
	require.NoError(t, err)
	defer form.RemoveAll()
	assert.Len(t, form.File["first_image"], 1)
}

// TestBridgeReferenceImageUploadValidatesBeforeUploading proves the pinned
// decoder runs on a placeholder copy *first*: a request the decoder rejects
// never reaches the image host.
func TestBridgeReferenceImageUploadValidatesBeforeUploading(t *testing.T) {
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	var seen map[string][]string
	reject := func(fields map[string][]string) error {
		seen = fields
		return assert.AnError
	}
	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, reject)
	require.Error(t, err)
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
	assert.Zero(t, uploads, "a request the decoder rejects must never be uploaded")
	assert.Empty(t, protocolFields(c)["first_image"], "a rejected request must not be rewritten")
	assert.NotEmpty(t, seen["first_image"], "the decoder must see a placeholder for every planned file")
	assert.Contains(t, seen["first_image"][0], refImageUploadPlaceholderSegment)
}

// TestBridgeReferenceImageUploadRejectsInvalidModelParametersBeforeUpload
// covers the field matrix: a request the plugin cannot decode (unknown model
// parameters, a model that takes no image) must produce zero uploads.
func TestBridgeReferenceImageUploadRejectsInvalidModelParametersBeforeUpload(t *testing.T) {
	tests := []struct {
		name     string
		verifier RefImageUploadVerifier
	}{
		{name: "decoder rejects unknown parameter", verifier: func(map[string][]string) error {
			return assert.AnError
		}},
		{name: "verifier unavailable", verifier: nil},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			uploads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uploads++
				_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
			}))
			defer server.Close()
			enableRefImageUploadConfig(t, server.URL+"/api/upload")

			c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
			pinRefImageUploadProtocolContext(c, map[string][]string{})

			err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, testCase.verifier)
			require.Error(t, err)
			assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
			assert.Zero(t, uploads)
			assert.Empty(t, protocolFields(c)["first_image"])
		})
	}
}

// TestBridgeReferenceImageUploadSurfacesDecoderMessageBeforeUpload verifies
// that a plugin-authored decode error reaches the caller with the rejection
// prefix, while engine-level failures keep the neutral message. Either way
// nothing is uploaded.
func TestBridgeReferenceImageUploadSurfacesDecoderMessageBeforeUpload(t *testing.T) {
	decoderError := func(message string) error {
		return fmt.Errorf("plugin decoder rejected the request: %w", &pluginruntime.HookError{
			Hook:    "protocols.openai_video.decodeRequest",
			Message: message,
		})
	}
	tests := []struct {
		name     string
		verifier RefImageUploadVerifier
		want     string
	}{
		{
			name: "plugin validation message is passed through",
			verifier: func(map[string][]string) error {
				return decoderError("field duration is required and must be an integer number of seconds")
			},
			want: RefImageUploadRejectedPrefix + "field duration is required and must be an integer number of seconds",
		},
		{
			name: "urls and the placeholder segment are stripped",
			verifier: func(map[string][]string) error {
				return decoderError("first_image https://z.lietio.com/" + refImageUploadPlaceholderSegment + "/pending-0 is not accepted")
			},
			want: RefImageUploadRejectedPrefix + "first_image [url] is not accepted",
		},
		{
			name:     "empty plugin message keeps the neutral text",
			verifier: func(map[string][]string) error { return decoderError("   ") },
			want:     RefImageUploadNeutralMessage,
		},
		{
			name: "engine failure without plugin text keeps the neutral text",
			verifier: func(map[string][]string) error {
				return errors.New("plugin decoder rejected the request: plugin call timed out")
			},
			want: RefImageUploadNeutralMessage,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			uploads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uploads++
				_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
			}))
			defer server.Close()
			enableRefImageUploadConfig(t, server.URL+"/api/upload")

			c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
			pinRefImageUploadProtocolContext(c, map[string][]string{})

			err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, testCase.verifier)
			require.Error(t, err)
			assert.Equal(t, testCase.want, err.Error())
			assert.Zero(t, uploads)
			assert.Empty(t, protocolFields(c)["first_image"])
		})
	}
}

// TestBridgeReferenceImageUploadRejectsExcessImagesBeforeUpload covers the
// per-model image count matrix at the service boundary.
func TestBridgeReferenceImageUploadRejectsExcessImagesBeforeUpload(t *testing.T) {
	previous := RefImageUploadMaxFiles
	RefImageUploadMaxFiles = 2
	t.Cleanup(func() { RefImageUploadMaxFiles = previous })

	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	file := refImageUploadTestPNGFile(t)
	files := []refImageUploadTestFile{
		{field: "referenceImages", filename: "a.png", contentType: "image/png", content: file.content},
		{field: "referenceImages", filename: "b.png", contentType: "image/png", content: file.content},
		{field: "referenceImages", filename: "c.png", contentType: "image/png", content: file.content},
	}
	c := refImageUploadTestContext(t, files, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
	require.Error(t, err)
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
	assert.Zero(t, uploads, "an over-count request must be rejected before any upload")
}

// TestBridgeReferenceImageUploadRejectsSingleFieldAliasConflict covers the
// first_image alias group: a hosted URL plus a file, or two aliases, must be
// rejected before any upload rather than silently overwritten.
func TestBridgeReferenceImageUploadRejectsSingleFieldAliasConflict(t *testing.T) {
	tests := []struct {
		name    string
		fields  map[string][]string
		extra   []refImageUploadTestFile
		message string
	}{
		{
			name:   "url and file for first_image",
			fields: map[string][]string{"first_image": {"https://z.lietio.com/existing.png"}},
			extra: []refImageUploadTestFile{
				{field: "first_image", filename: "a.png", contentType: "image/png"},
			},
			message: "a hosted URL and a file must not be merged",
		},
		{
			name:   "two aliases both targeting first_image",
			fields: map[string][]string{"input_reference": {"https://z.lietio.com/existing.png"}},
			extra: []refImageUploadTestFile{
				{field: "image", filename: "a.png", contentType: "image/png"},
			},
			message: "two aliases of a single-value field must not be merged",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			uploads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uploads++
				_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
			}))
			defer server.Close()
			enableRefImageUploadConfig(t, server.URL+"/api/upload")

			png := refImageUploadTestImageBytes(t, "image/png")
			for index := range testCase.extra {
				testCase.extra[index].content = png
			}
			c := refImageUploadTestContext(t, testCase.extra, nil)
			pinRefImageUploadProtocolContext(c, testCase.fields)

			err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
			require.Error(t, err, testCase.message)
			assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
			assert.Zero(t, uploads)
		})
	}
}

// TestBridgeReferenceImageUploadRejectsSpoofedMimeType covers MIME handling:
// the decision comes from the bytes, so a non-image labelled as an image is
// refused, and an image with a generic label is still recognised.
func TestBridgeReferenceImageUploadRejectsSpoofedMimeType(t *testing.T) {
	png := refImageUploadTestImageBytes(t, "image/png")
	tests := []struct {
		name     string
		file     refImageUploadTestFile
		accepted bool
		reason   string
	}{
		{
			name:     "arbitrary bytes declared as png",
			file:     refImageUploadTestFile{field: "first_image", filename: "a.png", contentType: "image/png", content: []byte("#!/bin/sh\necho not-an-image\n")},
			accepted: false,
			reason:   "a payload that is not an image must not pass under an image label",
		},
		{
			name:     "png bytes declared as jpeg",
			file:     refImageUploadTestFile{field: "first_image", filename: "a.jpg", contentType: "image/jpeg", content: png},
			accepted: false,
			reason:   "a declared image type that contradicts the bytes must be rejected",
		},
		{
			name:     "png bytes declared as svg",
			file:     refImageUploadTestFile{field: "first_image", filename: "a.svg", contentType: "image/svg+xml", content: png},
			accepted: true,
			reason:   "a non-allowlisted declaration is ignored; the sniffed type decides",
		},
		{
			name:     "png bytes with generic declaration",
			file:     refImageUploadTestFile{field: "first_image", filename: "a.png", contentType: "application/octet-stream", content: png},
			accepted: true,
			reason:   "a generic declaration carries no information and is not a conflict",
		},
		{
			name:     "png bytes with no declaration",
			file:     refImageUploadTestFile{field: "first_image", filename: "a.png", contentType: "", content: png},
			accepted: true,
			reason:   "an absent declaration is not a conflict",
		},
		{
			name:     "truncated png header only",
			file:     refImageUploadTestFile{field: "first_image", filename: "a.png", contentType: "image/png", content: append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{0}, 16)...)},
			accepted: false,
			reason:   "a signature without a decodable header must be rejected",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			uploads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uploads++
				_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
			}))
			defer server.Close()
			enableRefImageUploadConfig(t, server.URL+"/api/upload")

			c := refImageUploadTestContext(t, []refImageUploadTestFile{testCase.file}, nil)
			pinRefImageUploadProtocolContext(c, map[string][]string{})

			err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
			if testCase.accepted {
				require.NoError(t, err, testCase.reason)
				assert.Equal(t, 1, uploads)
				return
			}
			require.Error(t, err, testCase.reason)
			assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
			assert.Zero(t, uploads, testCase.reason)
		})
	}
}

// TestBridgeReferenceImageUploadAcceptsRealFormats keeps the allowlist honest:
// every allowlisted format is recognised from bytes alone.
func TestBridgeReferenceImageUploadAcceptsRealFormats(t *testing.T) {
	tests := []struct {
		format string
		bytes  []byte
	}{
		{format: "image/png", bytes: refImageUploadTestImageBytes(t, "image/png")},
		{format: "image/jpeg", bytes: refImageUploadTestImageBytes(t, "image/jpeg")},
		{format: "image/gif", bytes: refImageUploadTestImageBytes(t, "image/gif")},
		{format: "image/webp", bytes: refImageUploadTestWebPBytes(t)},
	}
	for _, testCase := range tests {
		t.Run(testCase.format, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.bin"}]}`))
			}))
			defer server.Close()
			enableRefImageUploadConfig(t, server.URL+"/api/upload")

			c := refImageUploadTestContext(t, []refImageUploadTestFile{
				{field: "first_image", filename: "a.bin", contentType: "", content: testCase.bytes},
			}, nil)
			pinRefImageUploadProtocolContext(c, map[string][]string{})
			require.NoError(t, BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload))
		})
	}
}

// TestBridgeReferenceImageUploadRejectsDecodeBomb covers the dimension guard: a
// small file declaring an enormous canvas must be refused.
func TestBridgeReferenceImageUploadRejectsDecodeBomb(t *testing.T) {
	// A 60000x60000 PNG IHDR costs almost nothing to build but would expand to
	// 3.6e9 pixels if forwarded. Only the header is written; no pixel data is
	// ever materialised here or in the bridge.
	header := pngHeaderBytes(t, 60000, 60000)

	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{
		{field: "first_image", filename: "bomb.png", contentType: "image/png", content: header},
	}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
	require.Error(t, err)
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
	assert.Zero(t, uploads, "an oversized canvas must be rejected before upload")
}

// pngHeaderBytes builds a valid PNG IHDR declaring the given dimensions without
// encoding any pixel data.
func pngHeaderBytes(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buffer bytes.Buffer
	buffer.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 0, 17)
	ihdr = append(ihdr, []byte("IHDR")...)
	for _, value := range []uint32{width, height} {
		ihdr = append(ihdr, byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
	}
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	length := []byte{0, 0, 0, 13}
	buffer.Write(length)
	buffer.Write(ihdr)
	buffer.Write([]byte{0, 0, 0, 0}) // CRC is not checked by DecodeConfig before it bails
	return buffer.Bytes()
}

// TestBridgeReferenceImageUploadIsIdempotent ensures a second call in the same
// request neither re-uploads nor duplicates the field value.
func TestBridgeReferenceImageUploadIsIdempotent(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	require.NoError(t, BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload))
	require.NoError(t, BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload))

	assert.Equal(t, 1, calls)
	assert.Equal(t, []string{"https://z.lietio.com/ref.png"}, protocolFields(c)["first_image"])
}

// TestBridgeReferenceImageUploadSkipsOtherPlugins covers the plugin gate.
func TestBridgeReferenceImageUploadSkipsOtherPlugins(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	require.NoError(t, BridgeReferenceImageUpload(c, "sora", allowRefImageUpload))
	require.NoError(t, BridgeReferenceImageUpload(c, "Seedance-2.0", allowRefImageUpload))
	require.NoError(t, BridgeReferenceImageUpload(c, "", allowRefImageUpload))

	assert.Zero(t, calls, "non-lietio plugins must never trigger an upload")
	assert.Empty(t, protocolFields(c)["first_image"])
}

// TestBridgeReferenceImageUploadSkipsFilelessAndNonMultipartRequests covers the
// request-shape gate.
func TestBridgeReferenceImageUploadSkipsFilelessAndNonMultipartRequests(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	fileless := refImageUploadTestContext(t, nil, map[string]string{"prompt": "hello"})
	pinRefImageUploadProtocolContext(fileless, map[string][]string{"prompt": {"hello"}})
	require.NoError(t, BridgeReferenceImageUpload(fileless, system_setting.RefImageUploadPluginKey, allowRefImageUpload))

	jsonContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	jsonContext.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"prompt":"hi"}`))
	jsonContext.Request.Header.Set("Content-Type", "application/json")
	pinRefImageUploadProtocolContext(jsonContext, map[string][]string{"prompt": {"hi"}})
	require.NoError(t, BridgeReferenceImageUpload(jsonContext, system_setting.RefImageUploadPluginKey, allowRefImageUpload))

	assert.Zero(t, calls)
}

// TestBridgeReferenceImageUploadDisabledDoesNothing covers the default-off
// configuration.
func TestBridgeReferenceImageUploadDisabledDoesNothing(t *testing.T) {
	t.Setenv(system_setting.RefImageUploadEnabledEnv, "false")
	system_setting.ResetRefImageUploadConfigCache()
	t.Cleanup(system_setting.ResetRefImageUploadConfigCache)

	c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	require.NoError(t, BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload))
	assert.Empty(t, protocolFields(c)["first_image"])
}

// TestBridgeReferenceImageUploadFailsClosedWithoutCredential ensures the
// bridge never uploads anonymously.
func TestBridgeReferenceImageUploadFailsClosedWithoutCredential(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	t.Setenv(system_setting.RefImageUploadEnabledEnv, "true")
	t.Setenv(system_setting.RefImageUploadEndpointEnv, server.URL+"/api/upload")
	system_setting.ResetRefImageUploadConfigCache()
	t.Cleanup(system_setting.ResetRefImageUploadConfigCache)

	c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
	require.Error(t, err)
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
	assert.Zero(t, calls)
}

// TestBridgeReferenceImageUploadRejectsHostFailureWithoutLeaking covers the
// failure path: a neutral message, no upstream detail, and no field rewrite.
func TestBridgeReferenceImageUploadRejectsHostFailureWithoutLeaking(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		handler http.HandlerFunc
	}{
		{name: "host 500", status: http.StatusInternalServerError, body: "upstream-secret-detail"},
		{name: "host 401", status: http.StatusUnauthorized, body: `{"message":"Invalid token"}`},
		{name: "host 413", status: http.StatusRequestEntityTooLarge, body: "too big"},
		{name: "empty files array", handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"files":[]}`))
		}},
		{name: "not json", handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		}},
		{name: "missing url", handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"files":[{"name":"ref.png"}]}`))
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			handler := testCase.handler
			if handler == nil {
				handler = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(testCase.status)
					_, _ = w.Write([]byte(testCase.body))
				}
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			enableRefImageUploadConfig(t, server.URL+"/api/upload")

			c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
			pinRefImageUploadProtocolContext(c, map[string][]string{})

			err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
			require.Error(t, err)
			assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
			assert.NotContains(t, err.Error(), "secret")
			assert.NotContains(t, err.Error(), "Invalid token")
			assert.Empty(t, protocolFields(c)["first_image"])
		})
	}
}

// TestBridgeReferenceImageUploadDoesNotFollowRedirects covers the redirect
// guard: a 3xx from the image host is a failure, and the credential must never
// be replayed against the redirect target.
func TestBridgeReferenceImageUploadDoesNotFollowRedirects(t *testing.T) {
	var redirectedToken string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedToken = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer target.Close()

	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/upload", http.StatusTemporaryRedirect)
	}))
	defer host.Close()
	enableRefImageUploadConfig(t, host.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
	require.Error(t, err, "a redirect must not be followed")
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
	assert.Empty(t, redirectedToken, "the upload credential must never be replayed against a redirect target")
	assert.Empty(t, protocolFields(c)["first_image"])
}

// TestRefImageUploadHTTPClientNeverInheritsProxy pins the transport policy: the
// upload credential must not travel through an ambient proxy.
func TestRefImageUploadHTTPClientNeverInheritsProxy(t *testing.T) {
	client := newRefImageUploadHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "the upload client must use a dedicated transport")
	assert.Nil(t, transport.Proxy, "the upload transport must not inherit the process proxy")
	require.NotNil(t, client.CheckRedirect)
	assert.Error(t, client.CheckRedirect(&http.Request{}, nil), "redirects must be refused")
	assert.Positive(t, transport.ResponseHeaderTimeout, "the response header wait must be bounded")
}

// TestBridgeReferenceImageUploadRejectsMaliciousResponseURL covers response URL
// validation: anything outside https://z.lietio.com/ is refused.
func TestBridgeReferenceImageUploadRejectsMaliciousResponseURL(t *testing.T) {
	rejected := []string{
		"https://evil.example/ref.png",
		"http://z.lietio.com/ref.png",
		"https://z.lietio.com.evil.example/ref.png",
		"https://user@z.lietio.com/ref.png",
		"https://z.lietio.com/ref.png?token=leak",
		"https://z.lietio.com/ref.png#frag",
		"https://z.lietio.com/",
		" https://z.lietio.com/ref.png",
		"https://z.lietio.com/ref.png\nX-Injected: yes",
		"javascript:alert(1)",
		"//z.lietio.com/ref.png",
		"",
	}
	for _, raw := range rejected {
		t.Run(raw, func(t *testing.T) {
			assert.False(t, ValidRefImageUploadURL(raw, system_setting.RefImageUploadPublicPrefix), "must be rejected")
		})
	}
	accepted := []string{
		"https://z.lietio.com/ref.png",
		"https://z.lietio.com/a/b/c.webp",
		"https://z.lietio.com/1f3/image.jpg",
	}
	for _, raw := range accepted {
		t.Run("accept "+raw, func(t *testing.T) {
			assert.True(t, ValidRefImageUploadURL(raw, system_setting.RefImageUploadPublicPrefix))
		})
	}
}

// TestBridgeReferenceImageUploadRejectsMaliciousURLFromHost proves the check is
// applied to the live response, not only to the helper.
func TestBridgeReferenceImageUploadRejectsMaliciousURLFromHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"url":"https://evil.example/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{refImageUploadTestPNGFile(t)}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
	require.Error(t, err)
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
	assert.NotContains(t, err.Error(), "evil.example")
	assert.Empty(t, protocolFields(c)["first_image"])
}

// TestBridgeReferenceImageUploadRejectsOversizedAndUnsupportedFiles covers the
// per-file type and size gates.
func TestBridgeReferenceImageUploadRejectsOversizedAndUnsupportedFiles(t *testing.T) {
	previous := RefImageUploadMaxFileBytes
	RefImageUploadMaxFileBytes = 64
	t.Cleanup(func() { RefImageUploadMaxFileBytes = previous })

	oversize := append(refImageUploadTestImageBytes(t, "image/png"), bytes.Repeat([]byte{0}, 256)...)
	tests := []struct {
		name string
		file refImageUploadTestFile
	}{
		{name: "unsupported type", file: refImageUploadTestFile{field: "first_image", filename: "a.pdf", contentType: "application/pdf", content: []byte("%PDF-1.4\n")}},
		{name: "missing type", file: refImageUploadTestFile{field: "first_image", filename: "a.bin", contentType: "", content: []byte("\x00\x01\x02\x03")}},
		{name: "svg type", file: refImageUploadTestFile{field: "first_image", filename: "a.svg", contentType: "image/svg+xml", content: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}},
		{name: "oversize", file: refImageUploadTestFile{field: "first_image", filename: "a.png", contentType: "image/png", content: oversize}},
		{name: "unknown field", file: refImageUploadTestFile{field: "referenceVideos", filename: "a.png", contentType: "image/png", content: refImageUploadTestImageBytes(t, "image/png")}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
			}))
			defer server.Close()
			enableRefImageUploadConfig(t, server.URL+"/api/upload")

			c := refImageUploadTestContext(t, []refImageUploadTestFile{testCase.file}, nil)
			pinRefImageUploadProtocolContext(c, map[string][]string{})

			err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
			require.Error(t, err)
			assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
			assert.Zero(t, calls, "a rejected file must never be uploaded")
		})
	}
}

// TestBridgeReferenceImageUploadRejectsTooManyFiles covers the file-count gate.
func TestBridgeReferenceImageUploadRejectsTooManyFiles(t *testing.T) {
	previous := RefImageUploadMaxFiles
	RefImageUploadMaxFiles = 1
	t.Cleanup(func() { RefImageUploadMaxFiles = previous })

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	png := refImageUploadTestImageBytes(t, "image/png")
	c := refImageUploadTestContext(t, []refImageUploadTestFile{
		{field: "referenceImages", filename: "a.png", contentType: "image/png", content: png},
		{field: "referenceImages", filename: "b.png", contentType: "image/png", content: png},
	}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
	require.Error(t, err)
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
	assert.Zero(t, calls)
}

// TestBridgeReferenceImageUploadRejectsDuplicateSingleFileField covers the
// first_image/last_image single-file contract.
func TestBridgeReferenceImageUploadRejectsDuplicateSingleFileField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	png := refImageUploadTestImageBytes(t, "image/png")
	c := refImageUploadTestContext(t, []refImageUploadTestFile{
		{field: "first_image", filename: "a.png", contentType: "image/png", content: png},
		{field: "first_image", filename: "b.png", contentType: "image/png", content: png},
	}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})

	err := BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload)
	require.Error(t, err)
	assert.Equal(t, RefImageUploadNeutralMessage, err.Error())
}

// TestBridgeReferenceImageUploadMapsReferenceImagesAndAliases covers the
// multi-image and alias field matrix.
func TestBridgeReferenceImageUploadMapsReferenceImagesAndAliases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	png := refImageUploadTestImageBytes(t, "image/png")
	cases := []struct {
		field  string
		target string
	}{
		{field: "referenceImages", target: "referenceImages"},
		{field: "referenceImages[]", target: "referenceImages"},
		{field: "images", target: "referenceImages"},
		{field: "input_reference", target: "first_image"},
		{field: "image", target: "first_image"},
		{field: "last_image", target: "last_image"},
	}
	for _, testCase := range cases {
		t.Run(testCase.field, func(t *testing.T) {
			c := refImageUploadTestContext(t, []refImageUploadTestFile{
				{field: testCase.field, filename: "a.png", contentType: "image/png", content: png},
			}, nil)
			pinRefImageUploadProtocolContext(c, map[string][]string{})
			require.NoError(t, BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload))
			assert.Equal(t, []string{"https://z.lietio.com/ref.png"}, protocolFields(c)[testCase.target])
		})
	}
}

// TestBridgeReferenceImageUploadUsesSanitizedFilename keeps control characters
// and path fragments from reaching the image host.
func TestBridgeReferenceImageUploadUsesSanitizedFilename(t *testing.T) {
	var filename string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		for _, headers := range r.MultipartForm.File {
			filename = headers[0].Filename
		}
		_, _ = w.Write([]byte(`{"files":[{"url":"https://z.lietio.com/ref.png"}]}`))
	}))
	defer server.Close()
	enableRefImageUploadConfig(t, server.URL+"/api/upload")

	c := refImageUploadTestContext(t, []refImageUploadTestFile{
		{field: "first_image", filename: `..\..\etc\passwd.png`, contentType: "image/png", content: refImageUploadTestImageBytes(t, "image/png")},
	}, nil)
	pinRefImageUploadProtocolContext(c, map[string][]string{})
	require.NoError(t, BridgeReferenceImageUpload(c, system_setting.RefImageUploadPluginKey, allowRefImageUpload))

	assert.Equal(t, "passwd.png", filename)
	assert.NotContains(t, filename, "..")
	assert.NotContains(t, filename, `\`)
}

func TestRefImageUploadStatusLineOmitsCredential(t *testing.T) {
	enableRefImageUploadConfig(t, "http://127.0.0.1:1/api/upload")
	line := RefImageUploadStatusLine()
	assert.Contains(t, line, "enabled=true")
	assert.Contains(t, line, "credential_source=env")
	assert.NotContains(t, line, refImageUploadTestToken)
}
