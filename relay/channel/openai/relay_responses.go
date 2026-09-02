package openai

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// responsesObservedOutput keeps a de-duplicated view of generated text-like
// fields seen on a Responses stream.  Providers normally send delta events
// followed by a full `*.done` value; counting both would overcharge a
// disconnected request.  Conversely, some compatible providers send only the
// done value, so it must be retained when no delta was observed.
type responsesObservedOutput struct {
	fields    map[string]*responsesObservedField
	order     []*responsesObservedField
	anonymous map[string][]*responsesObservedField
}

type responsesObservedField struct {
	key      string
	category string
	value    string
	sawDelta bool
}

func newResponsesObservedOutput() *responsesObservedOutput {
	return &responsesObservedOutput{
		fields:    make(map[string]*responsesObservedField),
		anonymous: make(map[string][]*responsesObservedField),
	}
}

func (o *responsesObservedOutput) field(category, key string, completeValue string, complete bool) *responsesObservedField {
	if o == nil || category == "" {
		return nil
	}
	if key != "" {
		mapKey := category + "\x00" + key
		if field := o.fields[mapKey]; field != nil {
			return field
		}
		field := &responsesObservedField{key: mapKey, category: category}
		o.fields[mapKey] = field
		o.order = append(o.order, field)
		return field
	}

	// Anonymous done events can still be matched to a preceding anonymous
	// delta by prefix/equality. This is important for providers which omit both
	// item_id and output_index on parallel tool calls.
	if complete {
		for _, field := range o.anonymous[category] {
			if field.value == "" || completeValue == "" {
				continue
			}
			if field.value == completeValue ||
				strings.HasPrefix(completeValue, field.value) ||
				strings.HasPrefix(field.value, completeValue) {
				return field
			}
		}
	} else if fields := o.anonymous[category]; len(fields) > 0 {
		return fields[len(fields)-1]
	}

	field := &responsesObservedField{
		key:      category + "\x00anon:" + strconv.Itoa(len(o.anonymous[category])),
		category: category,
	}
	o.anonymous[category] = append(o.anonymous[category], field)
	o.fields[field.key] = field
	o.order = append(o.order, field)
	return field
}

func (o *responsesObservedOutput) observeDelta(category, key, value string) {
	if value == "" {
		return
	}
	field := o.field(category, key, "", false)
	if field == nil {
		return
	}
	field.value += value
	field.sawDelta = true
}

func (o *responsesObservedOutput) observeComplete(category, key, value string) {
	if value == "" {
		return
	}
	field := (*responsesObservedField)(nil)
	if key != "" {
		field = o.fields[category+"\x00"+key]
		// A delta and its done event may identify the same item using different
		// fields (item_id vs output_index). Match by prefix before creating a
		// second accumulator, while still keeping distinct parallel values.
		if field == nil {
			for _, candidate := range o.order {
				if candidate == nil || candidate.category != category || candidate.value == "" {
					continue
				}
				if candidate.value == value || strings.HasPrefix(value, candidate.value) || strings.HasPrefix(candidate.value, value) {
					field = candidate
					break
				}
			}
		}
	}
	if field == nil {
		field = o.field(category, key, value, true)
	}
	if field == nil {
		return
	}
	if field.value == "" {
		field.value = value
		return
	}
	if field.value == value || strings.HasPrefix(field.value, value) {
		return
	}
	if strings.HasPrefix(value, field.value) {
		// The done value is a complete prefix-extended representation of the
		// deltas already observed. Append only its unseen suffix.
		field.value += value[len(field.value):]
		return
	}
	if !field.sawDelta {
		// A provider may send two item snapshots with different completeness;
		// retain the latest complete snapshot when no delta was counted.
		field.value = value
	}
	// If a delta and a non-prefix done value disagree, keep the observed
	// delta. Appending the full done value would almost certainly count it
	// twice; under-counting is bounded by the already observed work.
}

func (o *responsesObservedOutput) String() string {
	if o == nil || len(o.order) == 0 {
		return ""
	}
	var b strings.Builder
	for _, field := range o.order {
		if field != nil {
			b.WriteString(field.value)
			// Keep adjacent JSON/text fragments from becoming one lexical token
			// (e.g. a tool name immediately followed by `{`). This is still only
			// an estimate, but it avoids systematically under-counting done-only
			// and parallel tool output.
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// responsesToolCallTracker distinguishes an item.added/item.done pair from
// genuinely parallel anonymous calls. Explicit IDs/output indexes are used
// whenever available; anonymous calls are queued by type/name so two calls
// with the same name are not collapsed into one.
type responsesToolCallTracker struct {
	seen   map[string]struct{}
	active map[string][]string
	next   int
}

func newResponsesToolCallTracker() *responsesToolCallTracker {
	return &responsesToolCallTracker{
		seen:   make(map[string]struct{}),
		active: make(map[string][]string),
	}
}

func (t *responsesToolCallTracker) observe(item *dto.ResponsesOutput, eventType string, outputIndex *int) (bool, string) {
	if t == nil || item == nil {
		return false, ""
	}
	signature := item.Type + "\x00" + item.Name
	identity := responsesOutputIdentity(item, outputIndex)
	isAdded := eventType == dto.ResponsesOutputTypeItemAdded

	if isAdded {
		if identity == "" {
			identity = signature + "\x00anon:" + strconv.Itoa(t.next)
			t.next++
		}
		if _, exists := t.seen[identity]; exists {
			return false, identity
		}
		t.seen[identity] = struct{}{}
		t.active[signature] = append(t.active[signature], identity)
		return true, identity
	}

	if identity != "" {
		if _, exists := t.seen[identity]; exists {
			t.removeActive(signature, identity)
			return false, identity
		}
	}
	if active := t.active[signature]; len(active) > 0 {
		identity = active[0]
		t.active[signature] = active[1:]
		return false, identity
	}
	if identity == "" {
		identity = signature + "\x00done:" + strconv.Itoa(t.next)
		t.next++
	}
	t.seen[identity] = struct{}{}
	return true, identity
}

func (t *responsesToolCallTracker) removeActive(signature, identity string) {
	active := t.active[signature]
	for i, candidate := range active {
		if candidate == identity {
			t.active[signature] = append(active[:i], active[i+1:]...)
			return
		}
	}
}

func responsesOutputIdentity(item *dto.ResponsesOutput, outputIndex *int) string {
	if item == nil {
		return ""
	}
	if strings.TrimSpace(item.ID) != "" {
		return "item:" + strings.TrimSpace(item.ID)
	}
	if strings.TrimSpace(item.CallId) != "" {
		return "call:" + strings.TrimSpace(item.CallId)
	}
	if outputIndex != nil {
		return fmt.Sprintf("output:%d", *outputIndex)
	}
	return ""
}

func responsesTokenModel(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	if strings.TrimSpace(info.UpstreamModelName) != "" {
		return info.UpstreamModelName
	}
	// Unit callers and a few compatible channels can construct RelayInfo before
	// ChannelMeta is attached. Falling back to the origin model keeps token
	// estimation safe and avoids passing an empty model into an OpenAI encoder.
	return info.OriginModelName
}

func responsesEventFieldKey(event *dto.ResponsesStreamResponse, item *dto.ResponsesOutput) string {
	if event != nil && strings.TrimSpace(event.ItemID) != "" {
		return "item:" + strings.TrimSpace(event.ItemID)
	}
	if item != nil {
		if key := responsesOutputIdentity(item, eventOutputIndex(event)); key != "" {
			return key
		}
	}
	if event != nil && event.OutputIndex != nil {
		key := fmt.Sprintf("output:%d", *event.OutputIndex)
		if event.ContentIndex != nil {
			key += fmt.Sprintf(":content:%d", *event.ContentIndex)
		}
		if event.SummaryIndex != nil {
			key += fmt.Sprintf(":summary:%d", *event.SummaryIndex)
		}
		return key
	}
	return ""
}

func eventOutputIndex(event *dto.ResponsesStreamResponse) *int {
	if event == nil {
		return nil
	}
	return event.OutputIndex
}

func responsesObservedItemOutput(observed *responsesObservedOutput, item *dto.ResponsesOutput, outputIndex *int, fallbackKey string) {
	if observed == nil || item == nil {
		return
	}
	key := responsesOutputIdentity(item, outputIndex)
	if key == "" {
		key = fallbackKey
	}
	switch item.Type {
	case "message":
		for index, content := range item.Content {
			if content.Type == "output_text" || content.Type == "text" {
				contentKey := key
				if contentKey == "" {
					contentKey = fmt.Sprintf("content:%d", index)
				}
				observed.observeComplete("output_text", contentKey, content.Text)
			}
		}
	case "reasoning":
		for index, content := range item.Content {
			if content.Text == "" {
				continue
			}
			contentKey := key
			if contentKey == "" {
				contentKey = fmt.Sprintf("content:%d", index)
			}
			observed.observeComplete("reasoning", contentKey, content.Text)
		}
	case dto.BuildInCallFunctionCall:
		observed.observeComplete("function_name", key, item.Name)
		observed.observeComplete("function_arguments", key, item.ArgumentsString())
	case "custom_tool_call":
		observed.observeComplete("custom_tool_name", key, item.Name)
		input := item.Input
		if input == "" {
			input = item.ArgumentsString()
		}
		observed.observeComplete("custom_tool_input", key, input)
	case "code_interpreter_call":
		observed.observeComplete("code_interpreter_code", key, item.Code)
	}
}

// responsesOutputHasGeneratedOutput identifies fields that represent work
// produced by the model/tool call. It is used for terminal error events whose
// output item may be the only event we receive; without this signal the outer
// relay would treat a response.failed payload with no usage as a zero-work
// error and refund the entire reservation.
func responsesOutputHasGeneratedOutput(item *dto.ResponsesOutput) bool {
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

func responsesDeltaCategory(eventType string) string {
	switch eventType {
	case "response.output_text.delta", "response.refusal.delta":
		return "output_text"
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return "reasoning"
	case "response.function_call_arguments.delta":
		return "function_arguments"
	case "response.custom_tool_call_input.delta":
		return "custom_tool_input"
	case "response.code_interpreter_call_code.delta":
		return "code_interpreter_code"
	case "response.mcp_call_arguments.delta":
		return "mcp_arguments"
	case "response.audio_transcript.delta":
		return "audio_transcript"
	default:
		return ""
	}
}

func responsesDoneCategoryValue(event dto.ResponsesStreamResponse) (string, string) {
	switch event.Type {
	case "response.output_text.done", "response.refusal.done":
		if event.Text != "" {
			return "output_text", event.Text
		}
		return "output_text", event.Delta
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		if event.Text != "" {
			return "reasoning", event.Text
		}
		return "reasoning", event.Delta
	case "response.function_call_arguments.done":
		if event.Arguments != "" {
			return "function_arguments", event.Arguments
		}
		return "function_arguments", event.Delta
	case "response.custom_tool_call_input.done":
		if event.Input != "" {
			return "custom_tool_input", event.Input
		}
		return "custom_tool_input", event.Delta
	case "response.code_interpreter_call_code.done":
		return "code_interpreter_code", responsesAnyString(event.Code, event.Delta)
	case "response.mcp_call_arguments.done":
		if event.Arguments != "" {
			return "mcp_arguments", event.Arguments
		}
		return "mcp_arguments", event.Delta
	case "response.audio_transcript.done":
		if event.Transcript != "" {
			return "audio_transcript", event.Transcript
		}
		return "audio_transcript", event.Text
	default:
		return "", ""
	}
}

func responsesAnyString(value any, fallback string) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case nil:
		return fallback
	default:
		text := fmt.Sprint(typed)
		if text == "<nil>" || text == "" {
			return fallback
		}
		return text
	}
}

func applyResponsesUsage(target *dto.Usage, source *dto.Usage) bool {
	// Non-stream Responses payloads are complete response objects. Replace the
	// target wholesale so an explicitly reported zero output count remains
	// authoritative (e.g. a tool-only response).
	return applyResponsesUsageMode(target, source, true)
}

// applyResponsesUsageForEvent applies the usage semantics of a stream event.
// Normal completion events may legitimately report output_tokens=0, whereas
// cancellation/incomplete events are authoritative only when both input and
// output dimensions are present.
func applyResponsesUsageForEvent(target *dto.Usage, source *dto.Usage, eventType string) bool {
	return applyResponsesUsageMode(target, source, responsesUsageIsAuthoritativeTerminal(eventType, source))
}

func applyResponsesUsageMode(target *dto.Usage, source *dto.Usage, complete bool) bool {
	if target == nil || source == nil {
		return false
	}

	// A cancellation/incomplete event may carry only one usage dimension. Do
	// not assign the whole struct in that case: doing so erases output already
	// observed in preceding delta/tool events. A nominally complete terminal
	// object with output_tokens=0 is also merged when output was already
	// observed locally; compatible providers occasionally omit output from that
	// object even though the stream contains generated text/reasoning.
	mergeInsteadOfReplace := complete && responsesOutputTokens(source) == 0 && responsesOutputTokens(target) > 0
	if complete && !mergeInsteadOfReplace {
		*target = *source
	} else {
		mergeResponsesUsage(target, source)
	}

	inputTokens := responsesInputTokens(source)
	if inputTokens != 0 {
		target.PromptTokens = inputTokens
		target.InputTokens = inputTokens
	}
	outputTokens := responsesOutputTokens(source)
	if outputTokens != 0 {
		target.CompletionTokens = outputTokens
		target.OutputTokens = outputTokens
	}

	// For partial usage, recompute the total from all dimensions retained so
	// far. A provider's partial total (usually input-only) must not overwrite a
	// previously observed output count. For complete usage, preserve the
	// provider total when present and derive it only when omitted.
	if complete && !mergeInsteadOfReplace && source.TotalTokens > 0 {
		target.TotalTokens = source.TotalTokens
	} else {
		target.TotalTokens = safeResponsesTokenTotal(target.PromptTokens, target.CompletionTokens)
	}
	sanitizeResponsesUsage(target)

	return service.ValidUsage(target)
}

func responsesInputTokens(source *dto.Usage) int {
	if source == nil {
		return 0
	}
	if source.InputTokens != 0 {
		return source.InputTokens
	}
	if source.PromptTokens != 0 {
		return source.PromptTokens
	}
	// A few compatible Responses providers emit only token-detail objects.
	// Derive the aggregate rather than treating that usage as absent; this is
	// still conservative because all detail categories are counted once.
	details := source.PromptTokensDetails
	if source.InputTokensDetails != nil {
		details = *source.InputTokensDetails
	}
	return safeResponsesDetailTotal(details.CachedTokens, details.CachedCreationTokens,
		details.CacheWriteTokens, details.TextTokens, details.AudioTokens, details.ImageTokens)
}

func responsesOutputTokens(source *dto.Usage) int {
	if source == nil {
		return 0
	}
	if source.OutputTokens != 0 {
		return source.OutputTokens
	}
	if source.CompletionTokens != 0 {
		return source.CompletionTokens
	}
	details := source.CompletionTokenDetails
	if output := safeResponsesDetailTotal(details.TextTokens, details.AudioTokens, details.ImageTokens, details.ReasoningTokens); output > 0 {
		return output
	}
	// Some compatible providers omit output_tokens while still returning a
	// consistent total_tokens value. Recover the missing dimension when the
	// total is strictly larger than the known input; a tool-only response whose
	// total equals input remains zero.
	input := responsesInputTokens(source)
	if source.TotalTokens > input {
		return source.TotalTokens - input
	}
	return 0
}

func safeResponsesDetailTotal(values ...int) int {
	total := 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if total > math.MaxInt-value {
			return math.MaxInt
		}
		total += value
	}
	return total
}

// sanitizeResponsesUsage prevents malformed upstream negative counters from
// reducing a bill. The usage DTO is shared with the caller, so only the
// accumulated target is normalized; the original response payload remains
// untouched for diagnostics/logging.
func sanitizeResponsesUsage(usage *dto.Usage) {
	if usage == nil {
		return
	}
	if usage.PromptTokens < 0 {
		usage.PromptTokens = 0
	}
	if usage.CompletionTokens < 0 {
		usage.CompletionTokens = 0
	}
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.TotalTokens < 0 {
		usage.TotalTokens = 0
	}
	if usage.PromptCacheHitTokens < 0 {
		usage.PromptCacheHitTokens = 0
	}
	if usage.ClaudeCacheCreation5mTokens < 0 {
		usage.ClaudeCacheCreation5mTokens = 0
	}
	if usage.ClaudeCacheCreation1hTokens < 0 {
		usage.ClaudeCacheCreation1hTokens = 0
	}
	if usage.PromptTokensDetails.CachedTokens < 0 {
		usage.PromptTokensDetails.CachedTokens = 0
	}
	if usage.PromptTokensDetails.CachedCreationTokens < 0 {
		usage.PromptTokensDetails.CachedCreationTokens = 0
	}
	if usage.PromptTokensDetails.CacheWriteTokens < 0 {
		usage.PromptTokensDetails.CacheWriteTokens = 0
	}
	if usage.PromptTokensDetails.TextTokens < 0 {
		usage.PromptTokensDetails.TextTokens = 0
	}
	if usage.PromptTokensDetails.AudioTokens < 0 {
		usage.PromptTokensDetails.AudioTokens = 0
	}
	if usage.PromptTokensDetails.ImageTokens < 0 {
		usage.PromptTokensDetails.ImageTokens = 0
	}
	if usage.CompletionTokenDetails.TextTokens < 0 {
		usage.CompletionTokenDetails.TextTokens = 0
	}
	if usage.CompletionTokenDetails.AudioTokens < 0 {
		usage.CompletionTokenDetails.AudioTokens = 0
	}
	if usage.CompletionTokenDetails.ImageTokens < 0 {
		usage.CompletionTokenDetails.ImageTokens = 0
	}
	if usage.CompletionTokenDetails.ReasoningTokens < 0 {
		usage.CompletionTokenDetails.ReasoningTokens = 0
	}
	if usage.InputTokensDetails != nil {
		details := usage.InputTokensDetails
		if details.CachedTokens < 0 {
			details.CachedTokens = 0
		}
		if details.CachedCreationTokens < 0 {
			details.CachedCreationTokens = 0
		}
		if details.CacheWriteTokens < 0 {
			details.CacheWriteTokens = 0
		}
		if details.TextTokens < 0 {
			details.TextTokens = 0
		}
		if details.AudioTokens < 0 {
			details.AudioTokens = 0
		}
		if details.ImageTokens < 0 {
			details.ImageTokens = 0
		}
	}
}

func safeResponsesTokenTotal(inputTokens, outputTokens int) int {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if inputTokens > math.MaxInt-outputTokens {
		return math.MaxInt
	}
	return inputTokens + outputTokens
}

func mergeResponsesUsage(target, source *dto.Usage) {
	if target == nil || source == nil {
		return
	}
	if source.PromptCacheHitTokens != 0 {
		target.PromptCacheHitTokens = source.PromptCacheHitTokens
	}
	if source.UsageSemantic != "" {
		target.UsageSemantic = source.UsageSemantic
	}
	if source.UsageSource != "" {
		target.UsageSource = source.UsageSource
	}
	if source.BillingUsage != nil {
		target.BillingUsage = source.BillingUsage
	}
	if source.Cost != nil {
		target.Cost = source.Cost
	}
	if source.ClaudeCacheCreation5mTokens != 0 {
		target.ClaudeCacheCreation5mTokens = source.ClaudeCacheCreation5mTokens
	}
	if source.ClaudeCacheCreation1hTokens != 0 {
		target.ClaudeCacheCreation1hTokens = source.ClaudeCacheCreation1hTokens
	}
	mergeInputTokenDetails(&target.PromptTokensDetails, source.PromptTokensDetails)
	mergeOutputTokenDetails(&target.CompletionTokenDetails, source.CompletionTokenDetails)
	if source.InputTokensDetails != nil {
		details := *source.InputTokensDetails
		mergeInputTokenDetails(&target.PromptTokensDetails, details)
		target.InputTokensDetails = &details
	}
}

func mergeInputTokenDetails(target *dto.InputTokenDetails, source dto.InputTokenDetails) {
	if target == nil {
		return
	}
	if source.CachedTokens != 0 {
		target.CachedTokens = source.CachedTokens
	}
	if source.CachedCreationTokens != 0 {
		target.CachedCreationTokens = source.CachedCreationTokens
	}
	if source.CacheWriteTokens != 0 {
		target.CacheWriteTokens = source.CacheWriteTokens
	}
	if source.TextTokens != 0 {
		target.TextTokens = source.TextTokens
	}
	if source.AudioTokens != 0 {
		target.AudioTokens = source.AudioTokens
	}
	if source.ImageTokens != 0 {
		target.ImageTokens = source.ImageTokens
	}
}

func mergeOutputTokenDetails(target *dto.OutputTokenDetails, source dto.OutputTokenDetails) {
	if target == nil {
		return
	}
	if source.TextTokens != 0 {
		target.TextTokens = source.TextTokens
	}
	if source.AudioTokens != 0 {
		target.AudioTokens = source.AudioTokens
	}
	if source.ImageTokens != 0 {
		target.ImageTokens = source.ImageTokens
	}
	if source.ReasoningTokens != 0 {
		target.ReasoningTokens = source.ReasoningTokens
	}
}

// responsesUsageHasCompleteDimensions distinguishes a provider's complete
// terminal usage object from an input-only (or output-only) partial object.
// Responses providers commonly emit partial usage on cancellation; treating
// that object as authoritative would discard already observed reasoning/tool
// output and recreate the zero-billing bug this path is meant to prevent.
func responsesUsageHasCompleteDimensions(source *dto.Usage) bool {
	if source == nil {
		return false
	}
	inputTokens := responsesInputTokens(source)
	outputTokens := responsesOutputTokens(source)
	return inputTokens > 0 && outputTokens > 0
}

// responsesUsageIsAuthoritativeTerminal treats a normal completed response
// as authoritative even when output_tokens is legitimately zero (for example,
// a tool-only response). Abnormal terminal events must contain both input and
// output dimensions before they can suppress the conservative disconnect
// baseline; input-only/output-only cancellation payloads are partial.
func responsesUsageIsAuthoritativeTerminal(eventType string, source *dto.Usage) bool {
	if source == nil {
		return false
	}
	if eventType == "response.completed" || eventType == "response.done" {
		// A completed tool-only response can legitimately have zero output
		// tokens. Input (or an explicit non-zero total) is enough to establish
		// that this is a provider terminal usage object.
		return responsesInputTokens(source) > 0
	}
	return responsesUsageHasCompleteDimensions(source)
}

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := dto.Usage{}
	applyResponsesUsage(&usage, responsesResponse.Usage)
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	if !relaycommon.IsNonBillableResponsesStatus(responsesResponse.Status) {
		for i := range responsesResponse.Output {
			idx := i
			imageCounter.Observe(&responsesResponse.Output[i], &idx)
		}
	}
	imageCounter.Commit(info)

	return &usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	observedOutput := newResponsesObservedOutput()
	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	imageCommitted := false
	var streamErr *types.NewAPIError
	toolCallTracker := newResponsesToolCallTracker()
	hasUpstreamUsage := false
	hasCompleteUpstreamUsage := false
	terminalEventSeen := false
	abnormalTerminalEventSeen := false
	observedGeneratedOutput := false
	applyEventUsage := func(streamResponse *dto.ResponsesStreamResponse) {
		if streamResponse == nil {
			return
		}
		// The official Responses protocol nests usage under response, while
		// several compatible providers attach it directly to the SSE event
		// (especially response.error/response.failed). Prefer the nested value
		// when present and fall back to the top-level value otherwise.
		eventUsage := streamResponse.Usage
		if streamResponse.Response != nil && streamResponse.Response.Usage != nil {
			eventUsage = streamResponse.Response.Usage
		}
		if eventUsage == nil {
			return
		}
		if applyResponsesUsageForEvent(usage, eventUsage, streamResponse.Type) {
			hasUpstreamUsage = true
			hasCompleteUpstreamUsage = responsesUsageIsAuthoritativeTerminal(streamResponse.Type, eventUsage) || hasCompleteUpstreamUsage
		}
	}

	helper.StreamScannerHandlerWithDrain(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: error_meta="+common.SensitiveLogMeta(err.Error()))
			sr.Error(err)
			return
		}
		// Keep protocol-terminal state separate from StreamScannerHandler's
		// generic EOF/Done markers. A Responses stream that reaches EOF without
		// one of these events is truncated and must not silently refund its
		// reservation.
		switch streamResponse.Type {
		case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "response.error":
			terminalEventSeen = true
			if streamResponse.Type == "response.failed" || streamResponse.Type == "response.incomplete" ||
				streamResponse.Type == "response.cancelled" || streamResponse.Type == "response.canceled" {
				abnormalTerminalEventSeen = true
			}
			if streamResponse.Type == "response.error" {
				abnormalTerminalEventSeen = true
			}
		}
		if streamResponse.Type == "error" || streamResponse.Type == "response.failed" || streamResponse.Type == "response.error" {
			// A response.failed event may carry a partial response/usage object.
			// Process it before stopping the scanner; the old early return threw
			// away usage already emitted by the upstream and refunded the whole
			// reservation. A generic top-level error has no billable payload and
			// keeps the historical no-usage behaviour.
			applyEventUsage(&streamResponse)
			if streamResponse.Response != nil {
				for i := range streamResponse.Response.Output {
					idx := i
					output := &streamResponse.Response.Output[i]
					toolCounted, observationKey := toolCallTracker.observe(output, dto.ResponsesOutputTypeItemDone, &idx)
					if toolCounted {
						switch output.Type {
						case dto.BuildInCallWebSearchCall:
							info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
						case dto.BuildInCallFileSearchCall:
							info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
						case dto.BuildInCallFunctionCall:
							info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
						}
					}
					responsesObservedItemOutput(observedOutput, output, &idx, observationKey)
					if responsesOutputHasGeneratedOutput(output) {
						observedGeneratedOutput = true
					}
				}
			}
			if oaiErr := streamResponse.GetOpenAIError(); oaiErr != nil {
				streamErr = types.WithOpenAIError(*oaiErr, http.StatusInternalServerError)
			} else {
				streamErr = types.NewOpenAIError(
					fmt.Errorf("responses stream error: %s", streamResponse.Type),
					types.ErrorCodeBadResponse,
					http.StatusInternalServerError,
				)
			}
			// A downstream disconnect starts a bounded drain.  Error/failed
			// events are still useful during that window because compatible
			// providers may emit a final response.completed (with authoritative
			// usage) immediately afterwards, or may split usage across terminal
			// events.  Calling Stop here would terminate the data-handler
			// goroutine, close the drainDone signal, and make the scanner cleanup
			// race discard that tail.  Keep the error as a soft stream error while
			// draining; the outer handler still returns streamErr to the caller
			// once the bounded drain completes.
			if sr.IsDraining() {
				sr.Error(streamErr)
				return
			}
			sr.Stop(streamErr)
			return
		}
		switch streamResponse.Type {
		case "response.completed", "response.done":
			applyEventUsage(&streamResponse)
			if streamResponse.Response != nil {
				for i := range streamResponse.Response.Output {
					idx := i
					output := &streamResponse.Response.Output[i]
					toolCounted, observationKey := toolCallTracker.observe(output, dto.ResponsesOutputTypeItemDone, &idx)
					if toolCounted {
						switch output.Type {
						case dto.BuildInCallWebSearchCall:
							info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
						case dto.BuildInCallFileSearchCall:
							info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
						case dto.BuildInCallFunctionCall:
							info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
						}
					}
					responsesObservedItemOutput(observedOutput, output, &idx, observationKey)
					if output.ArgumentsString() != "" || output.Input != "" || output.Code != "" || output.Name != "" {
						observedGeneratedOutput = true
					}
				}
				if !imageCommitted {
					if relaycommon.IsNonBillableResponsesStatus(streamResponse.Response.Status) {
						imageCounter.Reset()
						imageCounter.Commit(info)
						imageCommitted = true
					} else {
						for i := range streamResponse.Response.Output {
							idx := i
							imageCounter.Observe(&streamResponse.Response.Output[i], &idx)
						}
						imageCounter.Commit(info)
						imageCommitted = true
					}
				}
			} else if !imageCommitted {
				imageCounter.Commit(info)
				imageCommitted = true
			}
		case "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
			applyEventUsage(&streamResponse)
			if streamResponse.Response != nil {
				for i := range streamResponse.Response.Output {
					idx := i
					_, observationKey := toolCallTracker.observe(&streamResponse.Response.Output[i], dto.ResponsesOutputTypeItemDone, &idx)
					responsesObservedItemOutput(observedOutput, &streamResponse.Response.Output[i], &idx, observationKey)
					if streamResponse.Response.Output[i].ArgumentsString() != "" || streamResponse.Response.Output[i].Input != "" || streamResponse.Response.Output[i].Code != "" || streamResponse.Response.Output[i].Name != "" {
						observedGeneratedOutput = true
					}
				}
			}
			if !imageCommitted {
				imageCounter.Reset()
				imageCounter.Commit(info)
				imageCommitted = true
			}
		case "response.output_text.delta",
			"response.reasoning_summary_text.delta",
			"response.reasoning_text.delta",
			"response.function_call_arguments.delta",
			"response.custom_tool_call_input.delta",
			"response.code_interpreter_call_code.delta",
			"response.mcp_call_arguments.delta":
			// Missing terminal usage must account for every observable generated
			// token category, not only assistant-visible output text.
			category := responsesDeltaCategory(streamResponse.Type)
			observedOutput.observeDelta(category, responsesEventFieldKey(&streamResponse, nil), streamResponse.Delta)
			if streamResponse.Delta != "" {
				observedGeneratedOutput = true
			}
		case "response.output_text.done",
			"response.reasoning_summary_text.done",
			"response.reasoning_text.done",
			"response.function_call_arguments.done",
			"response.custom_tool_call_input.done",
			"response.code_interpreter_call_code.done",
			"response.mcp_call_arguments.done",
			"response.audio_transcript.done":
			category, value := responsesDoneCategoryValue(streamResponse)
			if category != "" && value != "" {
				observedOutput.observeComplete(category, responsesEventFieldKey(&streamResponse, nil), value)
				observedGeneratedOutput = true
			}
		case "response.audio_transcript.delta":
			// Audio bytes themselves are base64/binary and must not be fed to a
			// text tokenizer. Transcript deltas are text and are billable output.
			observedOutput.observeDelta("audio_transcript", responsesEventFieldKey(&streamResponse, nil), streamResponse.Delta)
			if streamResponse.Delta != "" {
				observedGeneratedOutput = true
			}
		case "response.audio.delta":
			// No text-token estimate is safe for raw audio deltas. If the provider
			// omits usage, the incomplete-stream baseline still applies.
		case dto.ResponsesOutputTypeItemAdded, dto.ResponsesOutputTypeItemDone:
			if streamResponse.Item != nil {
				toolCounted, observationKey := toolCallTracker.observe(streamResponse.Item, streamResponse.Type, streamResponse.OutputIndex)
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					if toolCounted {
						info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
					}
				case dto.BuildInCallFileSearchCall:
					if toolCounted {
						info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
					}
				case dto.BuildInCallFunctionCall:
					if toolCounted {
						info.CountBillableToolCall(dto.BuildInCallFunctionCall, streamResponse.Item.Name)
					}
				case dto.ResponsesOutputTypeImageGenerationCall:
					if streamResponse.Type == dto.ResponsesOutputTypeItemDone && !imageCommitted {
						imageCounter.Observe(streamResponse.Item, streamResponse.OutputIndex)
					}
				}
				responsesObservedItemOutput(observedOutput, streamResponse.Item, streamResponse.OutputIndex, observationKey)
				if streamResponse.Item.ArgumentsString() != "" || streamResponse.Item.Input != "" || streamResponse.Item.Code != "" || streamResponse.Item.Name != "" {
					observedGeneratedOutput = true
				}
			}
		}
		// Once the downstream client is gone, keep parsing the bounded upstream
		// tail for a final usage event but never write to the cancelled response.
		// Writing here would turn the drain into a handler-stop and discard the
		// very usage data we are trying to recover.
		if sr.IsDraining() {
			return
		}
		if err := sendResponsesStreamData(c, streamResponse, data); err != nil {
			// ResponseChunkData returns an error when the downstream request has
			// just been cancelled. Mark that reason before StreamResult.Stop can
			// publish the generic handler_stop value; otherwise the two goroutines
			// race and billing/logging may classify a client disconnect as an
			// ordinary handler failure.
			if c != nil && c.Request != nil && c.Request.Context().Err() != nil && info.StreamStatus != nil {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
				// A write can race the cancellation notification observed by the
				// scanner's main loop. Keep the scanner alive for the opt-in bounded
				// drain; calling sr.Stop here would close dataChan and lose a final
				// authoritative usage event.
				if sr.IsDraining() || c.Request.Context().Err() != nil {
					return
				}
			}
			sr.Stop(err)
		}
	}, &helper.StreamScannerDrainOptions{})
	// StreamScannerHandler considers EOF/HandlerStop normal for legacy relay
	// formats. Publish Responses-specific terminal state before returning so
	// the billing layer can classify an abrupt EOF or an incomplete terminal
	// event correctly.
	streamIncomplete := abnormalTerminalEventSeen || !terminalEventSeen
	if info.StreamStatus != nil {
		switch info.StreamStatus.EndReason {
		case relaycommon.StreamEndReasonClientGone,
			relaycommon.StreamEndReasonTimeout,
			relaycommon.StreamEndReasonScannerErr,
			relaycommon.StreamEndReasonPanic,
			relaycommon.StreamEndReasonPingFail:
			streamIncomplete = true
		case relaycommon.StreamEndReasonEOF, relaycommon.StreamEndReasonHandlerStop:
			if !terminalEventSeen {
				streamIncomplete = true
			}
		}
	}
	common.SetContextKey(c, constant.ContextKeyResponsesStreamTerminalSeen, terminalEventSeen)
	common.SetContextKey(c, constant.ContextKeyResponsesStreamIncomplete, streamIncomplete)

	observedCompletionTokens := 0
	observedOutputText := observedOutput.String()
	if observedOutputText != "" {
		observedCompletionTokens = service.CountTextToken(observedOutputText, responsesTokenModel(info))
	}
	if observedCompletionTokens > usage.CompletionTokens {
		// A terminal usage object may legitimately report zero output for a
		// tool-only response, but if output text/reasoning was actually observed
		// it is safer to retain that observation than to let the zero overwrite
		// it. When the provider reported no output at all, the usage is no longer
		// fully authoritative and the incomplete-stream policy may apply on a
		// disconnect.
		providerReportedZeroOutput := usage.CompletionTokens == 0
		usage.CompletionTokens = observedCompletionTokens
		usage.OutputTokens = observedCompletionTokens
		if providerReportedZeroOutput {
			hasCompleteUpstreamUsage = false
		}
	}
	if !hasUpstreamUsage && usage.CompletionTokens == 0 {
		// 计算输出文本的 token 数量
		tempStr := observedOutputText
		if len(tempStr) > 0 {
			// 非正常结束，使用输出文本的 token 数量
			completionTokens := service.CountTextToken(tempStr, responsesTokenModel(info))
			usage.CompletionTokens = completionTokens
		}
	}

	if usage.PromptTokens == 0 && !hasCompleteUpstreamUsage {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}
	// Keep an explicit authority bit separate from local_count_tokens. A
	// Responses stream can need a local fallback for one missing dimension
	// while still carrying authoritative upstream usage for the dimensions it
	// did report. The billing layer uses this bit to decide whether the
	// incomplete-stream baseline is allowed to replace the usage object.
	// Any non-zero usage object supplied by the provider is authoritative for
	// the dimensions it contains. Do not let a locally estimated text delta (or
	// an estimated input fallback) overwrite that object; this matters most for
	// response.incomplete/response.cancelled events, whose usage may be partial
	// but is still the provider's accounting statement.
	authoritativeUsage := hasCompleteUpstreamUsage
	common.SetContextKey(c, constant.ContextKeyResponsesUsageAuthoritative, authoritativeUsage)
	common.SetContextKey(c, constant.ContextKeyLocalCountTokens, !authoritativeUsage)
	if streamErr != nil && (observedGeneratedOutput || observedOutputText != "" || hasUpstreamUsage) {
		// Let the outer Responses helper settle work that was exposed before an
		// upstream/protocol error. An error with no usage or generated output
		// keeps the ordinary full-refund path.
		common.SetContextKey(c, constant.ContextKeyResponsesPartialUsage, true)
	}

	if usage.InputTokens == 0 {
		usage.InputTokens = usage.PromptTokens
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = usage.CompletionTokens
	}
	usage.TotalTokens = safeResponsesTokenTotal(usage.PromptTokens, usage.CompletionTokens)

	if streamErr != nil {
		if common.GetContextKeyBool(c, constant.ContextKeyResponsesPartialUsage) {
			return usage, streamErr
		}
		return nil, streamErr
	}
	return usage, nil
}
