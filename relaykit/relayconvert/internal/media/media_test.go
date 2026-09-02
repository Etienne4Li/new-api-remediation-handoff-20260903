package media

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestResolveBase64DataValidatesResolverOutput(t *testing.T) {
	t.Cleanup(func() { SetMediaResolver(MediaResolver{}) })

	tests := []struct {
		name     string
		data     string
		mimeType string
		wantMIME string
		wantErr  string
	}{
		{name: "normalizes parameters", data: "aGVsbG8=", mimeType: "IMAGE/PNG; charset=binary", wantMIME: "image/png"},
		{name: "rejects invalid base64", data: "not_base64!", mimeType: "image/png", wantErr: "invalid resolved base64"},
		{name: "rejects active MIME", data: "PHN2Zz4=", mimeType: "image/svg+xml", wantErr: "unsafe resolved media MIME"},
		{name: "rejects unsupported MIME", data: "e30=", mimeType: "application/json", wantErr: "unsupported resolved media MIME"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			SetMediaResolver(MediaResolver{
				GetBase64Data: func(context.Context, types.FileSource, ...string) (string, string, error) {
					return test.data, test.mimeType, nil
				},
			})
			data, mimeType, err := ResolveBase64Data(context.Background(), types.NewBase64FileSource("ignored", ""))
			if test.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.data, data)
			require.Equal(t, test.wantMIME, mimeType)
		})
	}
}

func TestDecodeBase64FileDataRequiresImageOutput(t *testing.T) {
	t.Cleanup(func() { SetMediaResolver(MediaResolver{}) })
	SetMediaResolver(MediaResolver{
		DecodeBase64FileData: func(string) (string, string, error) {
			return "text/plain", "aGVsbG8=", nil
		},
	})

	_, _, err := DecodeBase64FileData("ignored")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an image")
}
