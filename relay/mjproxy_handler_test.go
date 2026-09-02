package relay

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaykitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMidjourneyImageRequestPropagatesCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	req, cancel, err := newMidjourneyImageRequest(parent, "https://cdn.example/image.png", time.Second)
	require.NoError(t, err)
	defer cancel()
	require.ErrorIs(t, req.Context().Err(), context.Canceled)
}

func TestNewMidjourneyImageRequestAppliesDeadline(t *testing.T) {
	req, cancel, err := newMidjourneyImageRequest(context.Background(), "https://cdn.example/image.png", time.Millisecond)
	require.NoError(t, err)
	defer cancel()
	select {
	case <-req.Context().Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("request context did not expire")
	}
	require.ErrorIs(t, req.Context().Err(), context.DeadlineExceeded)
}

func TestNewMidjourneyImageRequestRejectsMalformedURL(t *testing.T) {
	_, _, err := newMidjourneyImageRequest(context.Background(), "://bad", time.Second)
	require.Error(t, err)
}

func TestNewMidjourneyImageRequestUsesGET(t *testing.T) {
	req, cancel, err := newMidjourneyImageRequest(nil, "https://cdn.example/image.png", time.Second)
	require.NoError(t, err)
	defer cancel()
	require.Equal(t, http.MethodGet, req.Method)
}

func TestReadMidjourneyImageBodyEnforcesConfiguredLimit(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	probe := []byte("probe")
	body := bytes.Repeat([]byte{'x'}, (1<<20)-len(probe)+1)
	_, err := readMidjourneyImageBody(bytes.NewReader(body), int64(len(probe))+int64(len(body)), probe)
	require.ErrorIs(t, err, errMidjourneyImageBodyTooLarge)
}

func TestReadMidjourneyImageBodyAcceptsUnknownLengthWithinLimit(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	probe := []byte("probe")
	tail := bytes.Repeat([]byte{'x'}, 128)
	got, err := readMidjourneyImageBody(bytes.NewReader(tail), -1, probe)
	require.NoError(t, err)
	require.Equal(t, append(append([]byte(nil), probe...), tail...), got)
}

func TestReadMidjourneyImageBodyPropagatesReaderError(t *testing.T) {
	readErr := io.ErrUnexpectedEOF
	_, err := readMidjourneyImageBody(midjourneyErrorReader{err: readErr}, -1, []byte("probe"))
	require.ErrorIs(t, err, readErr)
}

type midjourneyErrorReader struct{ err error }

func (r midjourneyErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestMidjourneyChannelProxyFallsBackForNilSuccessfulLookup(t *testing.T) {
	assert.Empty(t, midjourneyChannelProxy(nil, nil))
}

func TestMidjourneyChannelProxyUsesValidChannelSetting(t *testing.T) {
	setting, err := common.Marshal(relaykitdto.ChannelSettings{Proxy: "http://proxy.example:8080"})
	require.NoError(t, err)
	settingJSON := string(setting)

	assert.Equal(t, "http://proxy.example:8080", midjourneyChannelProxy(&model.Channel{Setting: &settingJSON}, nil))
}

func TestValidateMidjourneyNotifyMediaRejectsUnsafeAndOversizedURLs(t *testing.T) {
	tests := []struct {
		name    string
		request *dto.MidjourneyDto
		wantErr bool
	}{
		{
			name:    "valid media",
			request: &dto.MidjourneyDto{ImageUrl: " https://cdn.example/image.png?sig=private ", VideoUrls: []dto.ImgUrls{{Url: "https://cdn.example/video.mp4"}}},
		},
		{
			name:    "unsafe image scheme",
			request: &dto.MidjourneyDto{ImageUrl: "file:///etc/passwd"},
			wantErr: true,
		},
		{
			name:    "userinfo is rejected",
			request: &dto.MidjourneyDto{VideoUrl: "https://user:pass@cdn.example/video.mp4"},
			wantErr: true,
		},
		{
			name:    "too many videos",
			request: &dto.MidjourneyDto{VideoUrls: make([]dto.ImgUrls, service.MaxMidjourneyVideoURLCount+1)},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMidjourneyNotifyMedia(tt.request)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "https://cdn.example/image.png?sig=private", tt.request.ImageUrl)
		})
	}
}

func TestCoverMidjourneyTaskDtoRedactsProviderFields(t *testing.T) {
	previousConfig := setting.GetMidjourneyConfig()
	previousAddress := system_setting.ServerAddress
	setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
		config.ForwardURLEnabled = false
	})
	system_setting.ServerAddress = "https://api.example.test/"
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
			*config = previousConfig
		})
	})

	task := &model.Midjourney{
		MjId:       "mj-1",
		ImageUrl:   "https://cdn.example/image.png?token=private",
		VideoUrl:   "https://cdn.example/video.mp4?sig=private",
		VideoUrls:  `[{"url":"https://cdn.example/clip.mp4?X-Amz-Signature=private"}]`,
		FailReason: "provider failed: Authorization: Bearer secret-token",
	}

	got := coverMidjourneyTaskDto(nil, task)

	assert.Equal(t, "https://cdn.example/image.png", got.ImageUrl)
	assert.Equal(t, "https://api.example.test/mj/video/mj-1", got.VideoUrl)
	assert.Equal(t, "provider failed: Authorization: Bearer ***", got.FailReason)
	require.Len(t, got.VideoUrls, 1)
	assert.Equal(t, "https://api.example.test/mj/video/mj-1/0", got.VideoUrls[0].Url)
	// The source row remains untouched so the authenticated image proxy can
	// continue using the provider's original signed URL.
	assert.Equal(t, "https://cdn.example/image.png?token=private", task.ImageUrl)

	task.ImageUrl = "javascript:alert(1)"
	got = coverMidjourneyTaskDto(nil, task)
	assert.Empty(t, got.ImageUrl)
}
