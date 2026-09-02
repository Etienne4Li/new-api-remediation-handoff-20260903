package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeProviderURLForStorageRemovesCredentialsOnly(t *testing.T) {
	got := SanitizeProviderURLForStorage(" https://user:pass@cdn.example/video.mp4?width=1280&token=private&X-Amz-Signature=signed#fragment ", MaxHTTPURLLength)
	assert.Equal(t, "https://cdn.example/video.mp4?width=1280", got)

	cloudFront := SanitizeProviderURLForStorage("https://cdn.example/file?Policy=private&Signature=signed&Key-Pair-Id=principal&download=1", MaxHTTPURLLength)
	assert.Equal(t, "https://cdn.example/file?download=1", cloudFront)

	azure := SanitizeProviderURLForStorage("https://blob.example/file?sv=1&se=2&sp=r&sr=b&sig=signed&name=clip", MaxHTTPURLLength)
	assert.Equal(t, "https://blob.example/file?name=clip", azure)
}

func TestNormalizeProviderURLForRuntimePreservesSignedQuery(t *testing.T) {
	got := NormalizeProviderURLForRuntime(" https://user:pass@cdn.example/video.mp4?width=1280&X-Amz-Signature=signed#fragment ", MaxHTTPURLLength)
	assert.Equal(t, "https://cdn.example/video.mp4?width=1280&X-Amz-Signature=signed", got)
}

func TestSanitizeProviderURLForStorageRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"https://cdn.example:99999/file",
		"https://cdn.example/file?token=%zz",
		strings.Repeat("x", 33),
	} {
		assert.Empty(t, SanitizeProviderURLForStorage(value, 32), "value=%q", value)
	}
}

func TestSanitizeProviderJSONForStoragePreservesBusinessMetadata(t *testing.T) {
	raw := `{"customId":"MJ::JOB::upsample::1::task-id","label":"U1","imageUrl":"https://cdn.example/image.png?width=1024&token=private","authorization":"Bearer private","nested":{"api_key":"secret","note":"failed Authorization: Bearer nested-secret"}}`
	got := SanitizeProviderJSONForStorage(raw, 1<<20)
	require.NotEmpty(t, got)
	assert.NotContains(t, got, "private")
	assert.NotContains(t, got, "nested-secret")
	assert.Contains(t, got, "MJ::JOB::upsample::1::task-id")
	assert.Contains(t, got, `"label":"U1"`)
	assert.Contains(t, got, "https://cdn.example/image.png?width=1024")
	assert.NotContains(t, got, "authorization")
	assert.NotContains(t, got, "api_key")
}
