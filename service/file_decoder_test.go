package service

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func TestGetFileTypeFromURLUsesDownloadedBytesBeforeHeaders(t *testing.T) {
	var pngData bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{B: 0xff, A: 0xff})
	require.NoError(t, png.Encode(&pngData, img))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mislabelled.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/actual.png":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(pngData.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalFetch := system_setting.GetFetchSetting()
	originalClient := httpClient
	t.Cleanup(func() {
		httpClient = originalClient
		_ = config.GlobalConfig.LoadFromDB(map[string]string{
			"fetch_setting.enable_ssrf_protection": fmt.Sprintf("%t", originalFetch.EnableSSRFProtection),
		})
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"fetch_setting.enable_ssrf_protection": "false",
	}))
	httpClient = server.Client()

	_, err := GetFileTypeFromUrl(nil, server.URL+"/mislabelled.png")
	require.ErrorIs(t, err, errUnsupportedFileMIME)

	mimeType, err := GetFileTypeFromUrl(nil, server.URL+"/actual.png")
	require.NoError(t, err)
	require.Equal(t, "image/png", mimeType)
}
