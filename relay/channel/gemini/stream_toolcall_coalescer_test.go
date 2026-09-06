package gemini

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func parseGeminiChunk(t *testing.T, raw string) *dto.GeminiChatResponse {
	t.Helper()
	var resp dto.GeminiChatResponse
	require.NoError(t, common.UnmarshalJsonStr(raw, &resp))
	return &resp
}

// Exact shape observed from the bblabu / oai.sb upstreams on 2026-09-06.
const (
	fragmentedChunk1 = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"web_fetch","args":{}}}]},"finishReason":null,"index":0}]}`
	fragmentedChunk2 = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"","args":{"url":"https://example.com"}}}]},"finishReason":null,"index":0}]}`
	fragmentedChunk3 = `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}]}`
)

func TestGeminiToolCallCoalescerMergesFragmentedCall(t *testing.T) {
	t.Parallel()
	s := newGeminiToolCallCoalescer()

	c1 := parseGeminiChunk(t, fragmentedChunk1)
	emit, changed := s.process(c1)
	require.False(t, emit, "chunk holding only the call header must be withheld")
	require.True(t, changed)

	c2 := parseGeminiChunk(t, fragmentedChunk2)
	emit, changed = s.process(c2)
	require.False(t, emit, "nameless fragment must be withheld")
	require.True(t, changed)
	require.True(t, s.Repaired)

	c3 := parseGeminiChunk(t, fragmentedChunk3)
	emit, changed = s.process(c3)
	require.True(t, emit)
	require.True(t, changed)
	require.Len(t, c3.Candidates, 1)
	parts := c3.Candidates[0].Content.Parts
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].FunctionCall)
	require.Equal(t, "web_fetch", parts[0].FunctionCall.FunctionName)
	require.Equal(t, map[string]any{"url": "https://example.com"}, parts[0].FunctionCall.Arguments)
	require.NotNil(t, c3.Candidates[0].FinishReason)
	require.Equal(t, "STOP", *c3.Candidates[0].FinishReason)
	require.Nil(t, s.flush())

	// The repaired chunk must convert into a single, named OpenAI tool call.
	oai, isStop := streamResponseGeminiChat2OpenAI(c3)
	require.True(t, isStop)
	require.Len(t, oai.Choices, 1)
	calls := oai.Choices[0].Delta.ToolCalls
	require.Len(t, calls, 1)
	require.Equal(t, "web_fetch", calls[0].Function.Name)
	require.JSONEq(t, `{"url":"https://example.com"}`, calls[0].Function.Arguments)
}

func TestGeminiToolCallCoalescerFlushesAtEOF(t *testing.T) {
	t.Parallel()
	s := newGeminiToolCallCoalescer()
	_, _ = s.process(parseGeminiChunk(t, fragmentedChunk1))
	_, _ = s.process(parseGeminiChunk(t, fragmentedChunk2))

	tail := s.flush()
	require.NotNil(t, tail)
	require.Len(t, tail.Candidates, 1)
	require.Equal(t, int64(0), tail.Candidates[0].Index)
	require.Equal(t, "STOP", *tail.Candidates[0].FinishReason)
	require.Len(t, tail.Candidates[0].Content.Parts, 1)
	require.Equal(t, "web_fetch", tail.Candidates[0].Content.Parts[0].FunctionCall.FunctionName)
	require.Equal(t, map[string]any{"url": "https://example.com"}, tail.Candidates[0].Content.Parts[0].FunctionCall.Arguments)
	require.Nil(t, s.flush(), "flush must be idempotent")
}

func TestGeminiToolCallCoalescerAtomicCallWithFinishIsUntouched(t *testing.T) {
	t.Parallel()
	s := newGeminiToolCallCoalescer()
	raw := `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"exec","args":{"command":"ls"}}}]},"finishReason":"STOP","index":0}]}`
	resp := parseGeminiChunk(t, raw)
	emit, _ := s.process(resp)
	require.True(t, emit)
	parts := resp.Candidates[0].Content.Parts
	require.Len(t, parts, 1)
	require.Equal(t, "exec", parts[0].FunctionCall.FunctionName)
	require.Equal(t, map[string]any{"command": "ls"}, parts[0].FunctionCall.Arguments)
	require.False(t, s.Repaired)
	require.Nil(t, s.flush())
}

func TestGeminiToolCallCoalescerReleasesCallBeforeTextAndParallelCalls(t *testing.T) {
	t.Parallel()
	s := newGeminiToolCallCoalescer()

	// Two named calls in one chunk: the first is released when the second arrives.
	two := parseGeminiChunk(t, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"a","args":{"x":1}}},{"functionCall":{"name":"b","args":{}}}]},"finishReason":null,"index":0}]}`)
	emit, changed := s.process(two)
	require.True(t, emit)
	require.True(t, changed)
	require.Len(t, two.Candidates[0].Content.Parts, 1)
	require.Equal(t, "a", two.Candidates[0].Content.Parts[0].FunctionCall.FunctionName)

	// A nameless fragment completes "b", then text forces its release ahead of the text.
	_, _ = s.process(parseGeminiChunk(t, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"","args":{"y":2}}}]},"finishReason":null,"index":0}]}`))
	text := parseGeminiChunk(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":null,"index":0}]}`)
	emit, _ = s.process(text)
	require.True(t, emit)
	parts := text.Candidates[0].Content.Parts
	require.Len(t, parts, 2)
	require.Equal(t, "b", parts[0].FunctionCall.FunctionName)
	require.Equal(t, map[string]any{"y": float64(2)}, parts[0].FunctionCall.Arguments)
	require.Equal(t, "done", parts[1].Text)
}

func TestGeminiToolCallCoalescerPlainTextStreamPassesThrough(t *testing.T) {
	t.Parallel()
	s := newGeminiToolCallCoalescer()
	for _, raw := range []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":null,"index":0}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]},"finishReason":"STOP","index":0}]}`,
		`{"candidates":[],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`,
	} {
		emit, changed := s.process(parseGeminiChunk(t, raw))
		require.True(t, emit)
		require.False(t, changed)
	}
	require.Nil(t, s.flush())
}

func TestGeminiToolCallCoalescerDropsOrphanFragment(t *testing.T) {
	t.Parallel()
	s := newGeminiToolCallCoalescer()
	resp := parseGeminiChunk(t, fragmentedChunk2)
	emit, changed := s.process(resp)
	require.False(t, emit)
	require.True(t, changed)
	require.Empty(t, resp.Candidates[0].Content.Parts)
}

func TestMergeGeminiFunctionArgs(t *testing.T) {
	t.Parallel()
	require.Equal(t, map[string]any{"a": 1}, mergeGeminiFunctionArgs(map[string]any{"a": 1}, map[string]any{}))
	require.Equal(t, map[string]any{"a": 1}, mergeGeminiFunctionArgs(map[string]any{"a": 1}, nil))
	require.Equal(t, map[string]any{"a": 1, "b": 2}, mergeGeminiFunctionArgs(map[string]any{"a": 1}, map[string]any{"b": 2}))
	require.Equal(t, map[string]any{"a": 9}, mergeGeminiFunctionArgs(map[string]any{"a": 1}, map[string]any{"a": 9}))
	require.Equal(t, map[string]any{"b": 2}, mergeGeminiFunctionArgs(nil, map[string]any{"b": 2}))
	require.Equal(t, map[string]any{}, mergeGeminiFunctionArgs(nil, nil))
	require.Equal(t, "raw", mergeGeminiFunctionArgs(map[string]any{"a": 1}, "raw"))
}
