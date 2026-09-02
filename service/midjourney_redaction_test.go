package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateMidjourneyMediaURLBoundsAndValidates(t *testing.T) {
	assert.NoError(t, ValidateMidjourneyMediaURL("https://cdn.example/image.png?token=private"))
	for _, value := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:image/png;base64,AAAA",
		"https://user:password@cdn.example/image.png",
		strings.Repeat("x", MaxMidjourneyMediaURLLength+1),
	} {
		assert.Error(t, ValidateMidjourneyMediaURL(value), "value=%q", value)
	}
}

func TestRedactMidjourneyMediaURLRemovesCredentialsAndUnsafeSchemes(t *testing.T) {
	assert.Equal(t, "https://cdn.example/image.png", RedactMidjourneyMediaURL("https://cdn.example/image.png?token=private#fragment"))
	assert.Empty(t, RedactMidjourneyMediaURL("javascript:alert(1)"))
	assert.Empty(t, RedactMidjourneyMediaURL("file:///etc/passwd"))
}

func TestRedactMidjourneyResponseBodyStrictlySanitizesURLFields(t *testing.T) {
	body := []byte(`{"url":"javascript:alert(1)","image_url":"file:///etc/passwd","location":"data:text/html,boom","result":"task-provider-id"}`)
	redacted := RedactMidjourneyResponseBody(body)
	assert.JSONEq(t, `{"url":"","image_url":"","location":"[redacted-data-url]","result":"task-provider-id"}`, string(redacted))
}
