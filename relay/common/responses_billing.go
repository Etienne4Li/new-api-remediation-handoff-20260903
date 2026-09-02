package common

import (
	"strings"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// ResponsesStreamBillingObservation is the small amount of protocol state the
// billing layer needs from a Responses-compatible stream.  It deliberately
// does not attempt to calculate quota; service.PostTextConsumeQuota owns that
// policy and can apply its conservative baseline when these markers say the
// stream was truncated or non-authoritative.
type ResponsesStreamBillingObservation struct {
	TerminalSeen       bool
	AbnormalTerminal   bool
	ObservedOutput     bool
	HasUpstreamUsage   bool
	AuthoritativeUsage bool
}

// ObserveResponsesEvent records terminal/usage/output signals from a native
// Responses event.  Compatible providers are inconsistent about whether usage
// is nested under response or attached to the event, so both locations are
// accepted.
func (o *ResponsesStreamBillingObservation) ObserveResponsesEvent(event *dto.ResponsesStreamResponse) {
	if o == nil || event == nil {
		return
	}

	switch event.Type {
	case "response.completed", "response.done", "response.failed", "response.incomplete",
		"response.cancelled", "response.canceled", "response.error":
		o.TerminalSeen = true
		if event.Type == "response.failed" || event.Type == "response.incomplete" ||
			event.Type == "response.cancelled" || event.Type == "response.canceled" ||
			event.Type == "response.error" {
			o.AbnormalTerminal = true
		}
	}

	usage := event.Usage
	if event.Response != nil && event.Response.Usage != nil {
		usage = event.Response.Usage
	}
	if usage != nil && usageHasTokens(usage) {
		o.HasUpstreamUsage = true
		if responsesUsageAuthoritative(event.Type, usage) {
			o.AuthoritativeUsage = true
		}
	}

	switch event.Type {
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta",
		"response.reasoning_text.delta", "response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta", "response.code_interpreter_call_code.delta",
		"response.mcp_call_arguments.delta", "response.audio_transcript.delta":
		o.ObservedOutput = o.ObservedOutput || event.Delta != ""
	case "response.output_text.done", "response.refusal.done", "response.reasoning_summary_text.done",
		"response.reasoning_text.done", "response.function_call_arguments.done",
		"response.custom_tool_call_input.done", "response.code_interpreter_call_code.done",
		"response.mcp_call_arguments.done", "response.audio_transcript.done":
		o.ObservedOutput = o.ObservedOutput || event.Delta != "" || event.Text != "" ||
			event.Arguments != "" || event.Input != "" || event.Transcript != ""
	case "response.output_item.added", "response.output_item.done":
		o.ObservedOutput = o.ObservedOutput || responsesOutputHasContent(event.Item)
	}
	if event.Response != nil {
		for i := range event.Response.Output {
			if responsesOutputHasContent(&event.Response.Output[i]) {
				o.ObservedOutput = true
				break
			}
		}
	}
}

// ObserveChatChunk records the equivalent signals when a provider speaks the
// legacy Chat Completions protocol but the public endpoint is Responses.
func (o *ResponsesStreamBillingObservation) ObserveChatChunk(chunk *dto.ChatCompletionsStreamResponse) {
	if o == nil || chunk == nil {
		return
	}
	if chunk.Usage != nil && usageHasTokens(chunk.Usage) {
		o.HasUpstreamUsage = true
		// Chat usage is a complete aggregate whenever it contains a prompt or
		// completion count (zero completion is valid for tool-only responses).
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.InputTokens > 0 {
			o.AuthoritativeUsage = true
		}
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
			o.TerminalSeen = true
		}
		delta := choice.Delta
		if delta.GetContentString() != "" || delta.GetReasoningContent() != "" || len(delta.ToolCalls) > 0 {
			o.ObservedOutput = true
		}
	}
}

// ObserveGeminiResponse records the equivalent state for Gemini's native
// generateContent stream before it is converted to Responses events. Gemini
// signals completion on candidate.finishReason rather than a Responses
// response.completed event.
func (o *ResponsesStreamBillingObservation) ObserveGeminiResponse(response *dto.GeminiChatResponse) {
	if o == nil || response == nil {
		return
	}
	if dto.HasGeminiUsageMetadataTokens(response.GetUsageMetadata()) {
		o.HasUpstreamUsage = true
		metadata := response.GetUsageMetadata()
		if metadata != nil && metadata.PromptTokenCount > 0 &&
			(metadata.CandidatesTokenCount > 0 || metadata.TotalTokenCount > 0) {
			o.AuthoritativeUsage = true
		}
	}
	if response.PromptFeedback != nil && response.PromptFeedback.BlockReason != nil {
		o.TerminalSeen = true
		o.AbnormalTerminal = true
	}
	for _, candidate := range response.Candidates {
		if candidate.FinishReason != nil && strings.TrimSpace(*candidate.FinishReason) != "" {
			o.TerminalSeen = true
		}
		for _, part := range candidate.Content.Parts {
			if part.Text != "" || part.InlineData != nil || part.FunctionCall != nil ||
				part.ExecutableCode != nil || part.CodeExecutionResult != nil {
				o.ObservedOutput = true
			}
		}
	}
}

// ApplyResponsesStreamBillingMarkers publishes the observation to the Gin
// context consumed by service/text_quota.go.  streamErr should be true when
// the adapter is returning a protocol/transport error after scanning events.
func (o *ResponsesStreamBillingObservation) ApplyResponsesStreamBillingMarkers(
	c *gin.Context,
	info *RelayInfo,
	streamErr bool,
	scannerDoneIsTerminal bool,
) {
	if c == nil {
		return
	}
	terminalSeen := false
	abnormal := false
	authoritative := false
	observed := false
	hasUsage := false
	if o != nil {
		terminalSeen = o.TerminalSeen
		abnormal = o.AbnormalTerminal
		authoritative = o.AuthoritativeUsage
		observed = o.ObservedOutput
		hasUsage = o.HasUpstreamUsage
	}

	if info != nil && info.StreamStatus != nil {
		switch info.StreamStatus.EndReason {
		case StreamEndReasonClientGone, StreamEndReasonTimeout, StreamEndReasonScannerErr,
			StreamEndReasonPanic, StreamEndReasonPingFail:
			abnormal = true
		case StreamEndReasonDone:
			// [DONE] is a source-protocol terminal marker for legacy Chat
			// Completions, but Responses requires response.completed/done (or an
			// explicit incomplete/cancelled event). Callers must opt in for the
			// former; otherwise a bare scanner sentinel must not hide truncation.
			if scannerDoneIsTerminal {
				terminalSeen = true
			}
		case StreamEndReasonEOF, StreamEndReasonHandlerStop:
			if !terminalSeen {
				abnormal = true
			}
		}
	}

	rootcommon.SetContextKey(c, constant.ContextKeyResponsesStreamTerminalSeen, terminalSeen)
	rootcommon.SetContextKey(c, constant.ContextKeyResponsesStreamIncomplete, abnormal || !terminalSeen)
	rootcommon.SetContextKey(c, constant.ContextKeyResponsesUsageAuthoritative, authoritative)
	rootcommon.SetContextKey(c, constant.ContextKeyLocalCountTokens, !authoritative)
	// Do not use the caller's fallback usage as evidence here: adapters often
	// synthesize prompt-token estimates after a clean protocol error.  Marking
	// that synthetic value as partial work would make a no-op error billable.
	if streamErr && (observed || hasUsage) {
		rootcommon.SetContextKey(c, constant.ContextKeyResponsesPartialUsage, true)
	}
}

// usageHasTokens treats either the canonical OpenAI fields or the normalized
// input/output aliases as evidence that an upstream usage object carried work.
func usageHasTokens(usage *dto.Usage) bool {
	if usage == nil {
		return false
	}
	return usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 ||
		usage.InputTokens != 0 || usage.OutputTokens != 0
}

func responsesUsageAuthoritative(eventType string, usage *dto.Usage) bool {
	if usage == nil {
		return false
	}
	input := usage.InputTokens
	if input == 0 {
		input = usage.PromptTokens
	}
	output := usage.OutputTokens
	if output == 0 {
		output = usage.CompletionTokens
	}
	if eventType == "response.completed" || eventType == "response.done" {
		return input > 0
	}
	return input > 0 && output > 0
}

func responsesOutputHasContent(item *dto.ResponsesOutput) bool {
	if item == nil {
		return false
	}
	if item.Name != "" || item.ArgumentsString() != "" || item.Input != "" || item.Code != "" {
		return true
	}
	for _, content := range item.Content {
		if content.Text != "" {
			return true
		}
	}
	return false
}
