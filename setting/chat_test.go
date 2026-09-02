package setting

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateChatsJSONAcceptsKeylessAndSupportedTemplates(t *testing.T) {
	value := `[{"Cherry":"cherrystudio://open?data={cherryConfig}"},{"Fluent":"fluentread"},{"CC":"ccswitch"},{"Web":"https://example.com/chat?server={address}"}]`
	require.NoError(t, ValidateChatsJSON(value))
}

func TestValidateChatsJSONRejectsMalformedOrUnsafeEntries(t *testing.T) {
	base := `[{"Chat":"https://example.com"}]`
	tests := []struct {
		name  string
		value string
	}{
		{name: "null", value: "null"},
		{name: "object", value: `{"Chat":"https://example.com"}`},
		{name: "multiple fields", value: `[{"Chat":"https://example.com","Other":"https://example.com"}]`},
		{name: "empty name", value: `[{"":"https://example.com"}]`},
		{name: "empty URL", value: `[{"Chat":""}]`},
		{name: "dangerous scheme", value: `[{"Chat":"javascript:alert(1)"}]`},
		{name: "userinfo", value: `[{"Chat":"https://user:pass@example.com"}]`},
		{name: "protocol relative", value: `[{"Chat":"//example.com"}]`},
		{name: "unsupported placeholder", value: `[{"Chat":"https://example.com/?value={unknown}"}]`},
		{name: "literal key", value: `[{"Chat":"https://example.com/?key=sk-abcdefgh"}]`},
		{name: "raw key", value: `[{"Chat":"https://example.com/?key=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`},
		{name: "sensitive query field", value: `[{"Chat":"https://example.com/?apiKey=plain-value"}]`},
		{name: "encoded sensitive JSON", value: `[{"Chat":"https://example.com/?config=eyJhcGlLZXkiOiJ4In0"}]`},
		{name: "malformed encoding", value: `[{"Chat":"https://example.com/?key=%zz"}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Error(t, ValidateChatsJSON(test.value))
		})
	}

	tooMany := "[" + strings.TrimSuffix(strings.Repeat(`{"Chat":"https://example.com"},`, maxChatsEntries+1), ",") + "]"
	assert.Error(t, ValidateChatsJSON(tooMany))
	assert.NoError(t, ValidateChatsJSON(base))
}

func TestUpdateChatsByJsonStringValidatesBeforePublish(t *testing.T) {
	previous := GetChats()
	t.Cleanup(func() {
		chatsMu.Lock()
		Chats = previous
		chatsMu.Unlock()
	})

	require.NoError(t, UpdateChatsByJsonString(`[{"Safe":"https://example.com"}]`))
	assert.Equal(t, []map[string]string{{"Safe": "https://example.com"}}, GetChats())
	assert.Error(t, UpdateChatsByJsonString(`[{"Safe":"https://example.com/?apiKey=leaked"}]`))
	assert.Equal(t, []map[string]string{{"Safe": "https://example.com"}}, GetChats())
	assert.Error(t, UpdateChatsByJsonString("null"))
}

func TestGetPublicChatsFiltersCredentialBearingPresets(t *testing.T) {
	previous := GetChats()
	t.Cleanup(func() {
		chatsMu.Lock()
		Chats = previous
		chatsMu.Unlock()
	})
	chatsMu.Lock()
	Chats = []map[string]string{
		{"safe": "https://example.com/chat"},
		{"placeholder": "client://open?key={key}"},
		{"literal": "client://open?key=sk-abcdefgh"},
		nil,
	}
	chatsMu.Unlock()

	assert.Equal(t, []map[string]string{{"safe": "https://example.com/chat"}}, GetPublicChats())

	chatsMu.Lock()
	Chats = nil
	chatsMu.Unlock()
	assert.NotNil(t, GetPublicChats())
	assert.Empty(t, GetPublicChats())
}

func TestChats2JsonStringUsesArrayForNil(t *testing.T) {
	previous := GetChats()
	t.Cleanup(func() {
		chatsMu.Lock()
		Chats = previous
		chatsMu.Unlock()
	})
	chatsMu.Lock()
	Chats = nil
	chatsMu.Unlock()
	assert.Equal(t, "[]", Chats2JsonString())
}

func TestSanitizeChatsJSONDropsUnsafeLegacyEntries(t *testing.T) {
	value := `[{"safe":"https://example.com"},{"unsafe":"https://example.com/?key=sk-abcdefgh"},{"old":"javascript:alert(1)"}]`
	assert.JSONEq(t, `[{"safe":"https://example.com"}]`, SanitizeChatsJSON(value))
	assert.JSONEq(t, "[]", SanitizeChatsJSON("not-json"))
}
