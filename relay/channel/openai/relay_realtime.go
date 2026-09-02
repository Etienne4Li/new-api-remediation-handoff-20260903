package openai

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if c == nil || info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}

	info.IsStream = true
	clientConn := info.ClientWs
	targetConn := info.TargetWs

	clientClosed := make(chan struct{})
	targetClosed := make(chan struct{})
	errChan := make(chan error, 2)
	var errMu sync.Mutex
	var firstErr error
	shuttingDown := false

	// Both websocket readers account for locally estimated usage concurrently.
	// Keep it behind an accumulator rather than sharing/reassigning raw pointers.
	localUsage := &realtimeUsageAccumulator{}
	sumUsage := &realtimeUsageAccumulator{}
	responseTracker := &realtimeResponseTracker{}
	var infoMu sync.RWMutex
	var readers sync.WaitGroup
	readers.Add(2)

	reportError := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if shuttingDown {
			errMu.Unlock()
			return
		}
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		select {
		case errChan <- err:
		default:
		}
	}

	countTokens := func(event *dto.RealtimeEvent) (int, int, error) {
		if event == nil {
			return 0, 0, fmt.Errorf("nil realtime event")
		}
		// CountTokenRealtime reads formats, tools, and IsFirstRequest while the
		// opposite reader may update them from session events.
		// CountTokenRealtime only needs a small mutable projection of RelayInfo.
		// Copy it while holding the lock, then do the potentially expensive audio
		// parsing/tokenisation without blocking session updates on the other reader.
		infoMu.RLock()
		countInfo := &relaycommon.RelayInfo{
			InputAudioFormat:  info.InputAudioFormat,
			OutputAudioFormat: info.OutputAudioFormat,
			IsFirstRequest:    info.IsFirstRequest,
			RealtimeTools:     append([]dto.RealTimeTool(nil), info.RealtimeTools...),
		}
		modelName := realtimeCountModelName(info)
		infoMu.RUnlock()
		return service.CountTokenRealtime(countInfo, *event, modelName)
	}

	commitLocalUsage := func() {
		pendingUsage, ok := localUsage.take()
		if ok {
			sumUsage.addUsage(pendingUsage)
		}
	}

	reserveCumulativeUsage := func() error {
		cumulativeUsage := sumUsage.snapshot()
		if !hasRealtimeUsage(cumulativeUsage) {
			return nil
		}
		// PreWssConsumeQuota raises the BillingSession reservation to the
		// cumulative target. It does not charge this frame independently.
		// PreWssConsumeQuota updates compatibility fields on RelayInfo. Keep the
		// entire operation under the same lock used by the websocket readers so a
		// session update cannot race a pricing/group snapshot or state sync.
		infoMu.Lock()
		defer infoMu.Unlock()
		return service.PreWssConsumeQuota(c, info, cumulativeUsage)
	}

	gopool.Go(func() {
		defer readers.Done()
		defer func() {
			if r := recover(); r != nil {
				reportError(fmt.Errorf("panic in client reader: %v", r))
			}
			close(clientClosed)
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := clientConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						reportError(fmt.Errorf("error reading from client: %v", err))
					}
					return
				}

				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					reportError(fmt.Errorf("error unmarshalling message: %v", err))
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate {
					if realtimeEvent.Session != nil {
						if realtimeEvent.Session.Tools != nil {
							infoMu.Lock()
							info.RealtimeTools = realtimeEvent.Session.Tools
							infoMu.Unlock()
						}
					}
				}

				textToken, audioToken, err := countTokens(realtimeEvent)
				if err != nil {
					reportError(fmt.Errorf("error counting text token: %v", err))
					return
				}
				logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
				localUsage.addTokenCounts(textToken, audioToken, true)

				err = helper.WssString(c, targetConn, string(message))
				if err != nil {
					reportError(fmt.Errorf("error writing to target: %v", err))
					return
				}

			}
		}
	})

	gopool.Go(func() {
		defer readers.Done()
		defer func() {
			if r := recover(); r != nil {
				reportError(fmt.Errorf("panic in target reader: %v", r))
			}
			close(targetClosed)
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := targetConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						reportError(fmt.Errorf("error reading from target: %v", err))
					}
					return
				}
				infoMu.Lock()
				info.SetFirstResponseTime()
				infoMu.Unlock()
				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					reportError(fmt.Errorf("error unmarshalling message: %v", err))
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeResponseDone {
					// A terminal frame can be replayed by an upstream proxy. Forward
					// every frame to the client, but account a response at most once.
					if !responseTracker.accept(realtimeEvent) {
						if err := helper.WssString(c, clientConn, string(message)); err != nil {
							reportError(fmt.Errorf("error writing to client: %v", err))
							return
						}
						continue
					}
					pendingUsage, _ := localUsage.rotate()
					realtimeUsage := realtimeResponseUsage(realtimeEvent)
					if realtimeUsage != nil {
						// Provider usage is per response/turn. It normally supersedes
						// local estimates, while the dimension-wise max fills fields
						// omitted by older providers or failed responses.
						merged := mergeRealtimeUsageWithEstimate(realtimeUsage, pendingUsage)
						if hasRealtimeUsage(&merged) {
							sumUsage.addUsage(&merged)
						}
					} else {
						textToken, audioToken, err := countTokens(realtimeEvent)
						if err != nil {
							reportError(fmt.Errorf("error counting text token: %v", err))
							return
						}
						logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						if pendingUsage == nil {
							pendingUsage = &dto.RealtimeUsage{}
						}
						addRealtimeTokenCounts(pendingUsage, textToken, audioToken, true)
						if hasRealtimeUsage(pendingUsage) {
							sumUsage.addUsage(pendingUsage)
						}
					}
					infoMu.Lock()
					info.IsFirstRequest = false
					infoMu.Unlock()
					if err := reserveCumulativeUsage(); err != nil {
						reportError(fmt.Errorf("error reserving cumulative usage: %v", err))
						return
					}
					logger.LogInfo(c, fmt.Sprintf("realtime streaming sumUsage: %v", sumUsage.snapshot()))
					logger.LogInfo(c, fmt.Sprintf("realtime streaming localUsage: %v", localUsage.snapshot()))

				} else if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdated || realtimeEvent.Type == dto.RealtimeEventTypeSessionCreated {
					realtimeSession := realtimeEvent.Session
					if realtimeSession != nil {
						// update audio format
						infoMu.Lock()
						info.InputAudioFormat = common.GetStringIfEmpty(realtimeSession.InputAudioFormat, info.InputAudioFormat)
						info.OutputAudioFormat = common.GetStringIfEmpty(realtimeSession.OutputAudioFormat, info.OutputAudioFormat)
						infoMu.Unlock()
					}
				} else {
					textToken, audioToken, err := countTokens(realtimeEvent)
					if err != nil {
						reportError(fmt.Errorf("error counting text token: %v", err))
						return
					}
					logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.addTokenCounts(textToken, audioToken, false)
				}

				err = helper.WssString(c, clientConn, string(message))
				if err != nil {
					reportError(fmt.Errorf("error writing to client: %v", err))
					return
				}

			}
		}
	})

	select {
	case <-clientClosed:
	case <-targetClosed:
	case err := <-errChan:
		//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
		logger.LogError(c, "realtime error: error_meta="+common.SensitiveLogMeta(err.Error()))
	case <-c.Done():
	}

	// From this point on socket errors are a consequence of our intentional
	// shutdown, not an additional upstream/client failure. Capture the first
	// real error before closing either connection so WssHelper can preserve its
	// failure semantics.
	errMu.Lock()
	shuttingDown = true
	errMu.Unlock()

	// The first reader to finish can leave the other one blocked in
	// ReadMessage. Close both sockets and wait for both workers before taking
	// the final snapshots, so no reader can mutate usage after return.
	_ = clientConn.Close()
	_ = targetConn.Close()
	readers.Wait()

	commitLocalUsage()
	if err := reserveCumulativeUsage(); err != nil {
		logger.LogError(c, "realtime final quota reservation failed: error_meta="+common.SensitiveLogMeta(err.Error()))
		errMu.Lock()
		if firstErr == nil {
			firstErr = fmt.Errorf("error reserving final cumulative usage: %w", err)
		}
		errMu.Unlock()
	}

	// check usage total tokens, if 0, use local usage
	usage := sumUsage.snapshot()
	errMu.Lock()
	terminalErr := firstErr
	errMu.Unlock()
	if terminalErr != nil {
		// The websocket may already have produced billable frames. Return the
		// usage snapshot together with the error so WssHelper can settle observed
		// usage before the controller reports the transport failure; callers then
		// retain the correct partial charge instead of blindly refunding all
		// pre-consumed quota.
		return types.NewErrorWithStatusCode(terminalErr, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry()), usage
	}
	return nil, usage
}

// realtimeCountModelName keeps token estimation usable for internal callers
// that construct RelayInfo before channel metadata is attached. The embedded
// ChannelMeta field is optional during setup, while OriginModelName is always
// available on a normal relay request.
func realtimeCountModelName(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	if info.ChannelMeta != nil && info.ChannelMeta.UpstreamModelName != "" {
		return info.ChannelMeta.UpstreamModelName
	}
	return info.OriginModelName
}

// realtimeUsageAccumulator owns a mutable usage value shared by the two
// websocket reader goroutines. A value (rather than a replaceable pointer)
// makes reset/take operations atomic with respect to concurrent token counts.
type realtimeUsageAccumulator struct {
	mu    sync.Mutex
	value dto.RealtimeUsage
}

func (a *realtimeUsageAccumulator) addTokenCounts(textTokens, audioTokens int, input bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	addRealtimeTokenCounts(&a.value, textTokens, audioTokens, input)
	a.mu.Unlock()
}

func (a *realtimeUsageAccumulator) addUsage(src *dto.RealtimeUsage) {
	if a == nil || src == nil {
		return
	}
	a.mu.Lock()
	addRealtimeUsage(&a.value, src)
	a.mu.Unlock()
}

func (a *realtimeUsageAccumulator) reset() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.value = dto.RealtimeUsage{}
	a.mu.Unlock()
}

func (a *realtimeUsageAccumulator) take() (*dto.RealtimeUsage, bool) {
	return a.rotate()
}

// rotate atomically returns usage observed before a response boundary and
// starts a fresh bucket for events that race in after that boundary. Keeping
// two buckets avoids the data-loss window caused by resetting a shared value
// while the opposite websocket reader is still accounting tokens.
func (a *realtimeUsageAccumulator) rotate() (*dto.RealtimeUsage, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.Lock()
	value := a.value
	a.value = dto.RealtimeUsage{}
	a.mu.Unlock()
	if !hasRealtimeUsage(&value) {
		return nil, false
	}
	return &value, true
}

func (a *realtimeUsageAccumulator) snapshot() *dto.RealtimeUsage {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	value := a.value
	a.mu.Unlock()
	return &value
}

func hasRealtimeUsage(usage *dto.RealtimeUsage) bool {
	if usage == nil {
		return false
	}
	value := normalizeRealtimeUsage(usage)
	return value.TotalTokens > 0 ||
		value.InputTokens > 0 ||
		value.OutputTokens > 0 ||
		value.InputTokenDetails.CachedTokens > 0 ||
		value.InputTokenDetails.TextTokens > 0 ||
		value.InputTokenDetails.AudioTokens > 0 ||
		value.InputTokenDetails.ImageTokens > 0 ||
		value.InputTokenDetails.CachedCreationTokens > 0 ||
		value.InputTokenDetails.CacheWriteTokens > 0 ||
		value.OutputTokenDetails.TextTokens > 0 ||
		value.OutputTokenDetails.AudioTokens > 0 ||
		value.OutputTokenDetails.ImageTokens > 0 ||
		value.OutputTokenDetails.ReasoningTokens > 0
}

func realtimeResponseUsage(event *dto.RealtimeEvent) *dto.RealtimeUsage {
	if event == nil || event.Response == nil || !hasRealtimeUsage(event.Response.Usage) {
		return nil
	}
	return event.Response.Usage
}

// realtimeResponseTracker fences duplicate terminal frames. Response IDs are
// preferred because a replay may be assigned a fresh event ID by an upstream
// proxy; event IDs remain a fallback for providers that omit response.id.
type realtimeResponseTracker struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (t *realtimeResponseTracker) accept(event *dto.RealtimeEvent) bool {
	if t == nil {
		return true
	}
	key := realtimeResponseIdentity(event)
	if key == "" {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = make(map[string]struct{})
	}
	if _, exists := t.seen[key]; exists {
		return false
	}
	// A websocket session is bounded in practice, but cap the map so a
	// malicious client cannot grow it without limit by sending unique IDs.
	if len(t.seen) >= 4096 {
		t.seen = make(map[string]struct{})
	}
	t.seen[key] = struct{}{}
	return true
}

func realtimeResponseIdentity(event *dto.RealtimeEvent) string {
	if event == nil {
		return ""
	}
	if event.Response != nil {
		if id := strings.TrimSpace(event.Response.Id); id != "" {
			return "response:" + id
		}
	}
	if id := strings.TrimSpace(event.EventId); id != "" {
		return "event:" + id
	}
	return ""
}

// addRealtimeTokenCounts adds locally-estimated text/audio tokens to one side
// of a realtime turn. Upstream events are untrusted, so every component is
// treated as non-negative and the aggregate saturates instead of wrapping.
func addRealtimeTokenCounts(usage *dto.RealtimeUsage, textTokens, audioTokens int, input bool) {
	if usage == nil {
		return
	}
	turnTokens := common.SaturatingAddNonNegativeInt(textTokens, audioTokens)
	usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.TotalTokens, turnTokens)
	if input {
		usage.InputTokens = common.SaturatingAddNonNegativeInt(usage.InputTokens, turnTokens)
		usage.InputTokenDetails.TextTokens = common.SaturatingAddNonNegativeInt(usage.InputTokenDetails.TextTokens, textTokens)
		usage.InputTokenDetails.AudioTokens = common.SaturatingAddNonNegativeInt(usage.InputTokenDetails.AudioTokens, audioTokens)
		return
	}
	usage.OutputTokens = common.SaturatingAddNonNegativeInt(usage.OutputTokens, turnTokens)
	usage.OutputTokenDetails.TextTokens = common.SaturatingAddNonNegativeInt(usage.OutputTokenDetails.TextTokens, textTokens)
	usage.OutputTokenDetails.AudioTokens = common.SaturatingAddNonNegativeInt(usage.OutputTokenDetails.AudioTokens, audioTokens)
}

// addRealtimeUsage merges an upstream usage frame into an aggregate while
// preserving all billable dimensions without allowing malformed values or
// repeated frames to overflow an int.
func addRealtimeUsage(dst, src *dto.RealtimeUsage) {
	if dst == nil || src == nil {
		return
	}
	normalized := normalizeRealtimeUsage(src)
	dst.TotalTokens = common.SaturatingAddNonNegativeInt(dst.TotalTokens, normalized.TotalTokens)
	dst.InputTokens = common.SaturatingAddNonNegativeInt(dst.InputTokens, normalized.InputTokens)
	dst.OutputTokens = common.SaturatingAddNonNegativeInt(dst.OutputTokens, normalized.OutputTokens)
	dst.InputTokenDetails.CachedTokens = common.SaturatingAddNonNegativeInt(dst.InputTokenDetails.CachedTokens, normalized.InputTokenDetails.CachedTokens)
	dst.InputTokenDetails.TextTokens = common.SaturatingAddNonNegativeInt(dst.InputTokenDetails.TextTokens, normalized.InputTokenDetails.TextTokens)
	dst.InputTokenDetails.AudioTokens = common.SaturatingAddNonNegativeInt(dst.InputTokenDetails.AudioTokens, normalized.InputTokenDetails.AudioTokens)
	dst.InputTokenDetails.ImageTokens = common.SaturatingAddNonNegativeInt(dst.InputTokenDetails.ImageTokens, normalized.InputTokenDetails.ImageTokens)
	dst.InputTokenDetails.CachedCreationTokens = common.SaturatingAddNonNegativeInt(dst.InputTokenDetails.CachedCreationTokens, normalized.InputTokenDetails.CachedCreationTokens)
	dst.InputTokenDetails.CacheWriteTokens = common.SaturatingAddNonNegativeInt(dst.InputTokenDetails.CacheWriteTokens, normalized.InputTokenDetails.CacheWriteTokens)
	dst.OutputTokenDetails.TextTokens = common.SaturatingAddNonNegativeInt(dst.OutputTokenDetails.TextTokens, normalized.OutputTokenDetails.TextTokens)
	dst.OutputTokenDetails.AudioTokens = common.SaturatingAddNonNegativeInt(dst.OutputTokenDetails.AudioTokens, normalized.OutputTokenDetails.AudioTokens)
	dst.OutputTokenDetails.ImageTokens = common.SaturatingAddNonNegativeInt(dst.OutputTokenDetails.ImageTokens, normalized.OutputTokenDetails.ImageTokens)
	dst.OutputTokenDetails.ReasoningTokens = common.SaturatingAddNonNegativeInt(dst.OutputTokenDetails.ReasoningTokens, normalized.OutputTokenDetails.ReasoningTokens)
}

// normalizeRealtimeUsage fills omitted aggregate/detail counters using the
// counters that are present. Providers occasionally send only input_tokens /
// output_tokens (or only detail fields); treating those frames as one token
// would undercharge an otherwise valid response.
func normalizeRealtimeUsage(src *dto.RealtimeUsage) dto.RealtimeUsage {
	if src == nil {
		return dto.RealtimeUsage{}
	}
	value := *src
	// Provider payloads are untrusted. Clamp every counter before deriving
	// aggregate fields; otherwise a negative detail can make a non-empty usage
	// frame look valid while contributing a malformed billing value.
	value.TotalTokens = nonNegativeRealtimeToken(value.TotalTokens)
	value.InputTokens = nonNegativeRealtimeToken(value.InputTokens)
	value.OutputTokens = nonNegativeRealtimeToken(value.OutputTokens)
	value.InputTokenDetails.CachedTokens = nonNegativeRealtimeToken(value.InputTokenDetails.CachedTokens)
	value.InputTokenDetails.TextTokens = nonNegativeRealtimeToken(value.InputTokenDetails.TextTokens)
	value.InputTokenDetails.AudioTokens = nonNegativeRealtimeToken(value.InputTokenDetails.AudioTokens)
	value.InputTokenDetails.ImageTokens = nonNegativeRealtimeToken(value.InputTokenDetails.ImageTokens)
	value.InputTokenDetails.CachedCreationTokens = nonNegativeRealtimeToken(value.InputTokenDetails.CachedCreationTokens)
	value.InputTokenDetails.CacheWriteTokens = nonNegativeRealtimeToken(value.InputTokenDetails.CacheWriteTokens)
	value.OutputTokenDetails.TextTokens = nonNegativeRealtimeToken(value.OutputTokenDetails.TextTokens)
	value.OutputTokenDetails.AudioTokens = nonNegativeRealtimeToken(value.OutputTokenDetails.AudioTokens)
	value.OutputTokenDetails.ImageTokens = nonNegativeRealtimeToken(value.OutputTokenDetails.ImageTokens)
	value.OutputTokenDetails.ReasoningTokens = nonNegativeRealtimeToken(value.OutputTokenDetails.ReasoningTokens)
	inputDetails := common.SaturatingAddNonNegativeInt(
		value.InputTokenDetails.TextTokens,
		value.InputTokenDetails.AudioTokens,
		value.InputTokenDetails.ImageTokens,
	)
	if inputDetails == 0 && value.InputTokens > 0 {
		value.InputTokenDetails.TextTokens = value.InputTokens
	}
	outputDetails := common.SaturatingAddNonNegativeInt(
		value.OutputTokenDetails.TextTokens,
		value.OutputTokenDetails.AudioTokens,
		value.OutputTokenDetails.ImageTokens,
	)
	if outputDetails == 0 && value.OutputTokens > 0 {
		value.OutputTokenDetails.TextTokens = value.OutputTokens
	}
	if value.InputTokens <= 0 {
		value.InputTokens = inputDetails
	} else if value.InputTokens < inputDetails {
		// Details are a decomposition of input usage. If a malformed provider
		// frame reports a smaller aggregate, retain the conservative detail sum.
		value.InputTokens = inputDetails
	}
	if value.OutputTokens <= 0 {
		value.OutputTokens = outputDetails
	} else if value.OutputTokens < outputDetails {
		value.OutputTokens = outputDetails
	}
	if value.TotalTokens <= 0 {
		value.TotalTokens = common.SaturatingAddNonNegativeInt(value.InputTokens, value.OutputTokens)
	} else {
		minimumTotal := common.SaturatingAddNonNegativeInt(value.InputTokens, value.OutputTokens)
		if value.TotalTokens < minimumTotal {
			value.TotalTokens = minimumTotal
		}
	}
	return value
}

func nonNegativeRealtimeToken(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// mergeRealtimeUsageWithEstimate uses the provider's per-response usage as
// the source of truth while filling omitted/partial dimensions from the local
// estimate. A max per dimension avoids summing the same audio/text tokens
// twice, and the aggregate fields are raised to remain internally consistent.
func mergeRealtimeUsageWithEstimate(provider, estimate *dto.RealtimeUsage) dto.RealtimeUsage {
	merged := normalizeRealtimeUsage(provider)
	estimated := normalizeRealtimeUsage(estimate)
	maxToken := func(a, b int) int {
		if a >= b {
			return a
		}
		return b
	}
	merged.TotalTokens = maxToken(merged.TotalTokens, estimated.TotalTokens)
	merged.InputTokens = maxToken(merged.InputTokens, estimated.InputTokens)
	merged.OutputTokens = maxToken(merged.OutputTokens, estimated.OutputTokens)
	merged.InputTokenDetails.CachedTokens = maxToken(merged.InputTokenDetails.CachedTokens, estimated.InputTokenDetails.CachedTokens)
	merged.InputTokenDetails.TextTokens = maxToken(merged.InputTokenDetails.TextTokens, estimated.InputTokenDetails.TextTokens)
	merged.InputTokenDetails.AudioTokens = maxToken(merged.InputTokenDetails.AudioTokens, estimated.InputTokenDetails.AudioTokens)
	merged.InputTokenDetails.ImageTokens = maxToken(merged.InputTokenDetails.ImageTokens, estimated.InputTokenDetails.ImageTokens)
	merged.InputTokenDetails.CachedCreationTokens = maxToken(merged.InputTokenDetails.CachedCreationTokens, estimated.InputTokenDetails.CachedCreationTokens)
	merged.InputTokenDetails.CacheWriteTokens = maxToken(merged.InputTokenDetails.CacheWriteTokens, estimated.InputTokenDetails.CacheWriteTokens)
	merged.OutputTokenDetails.TextTokens = maxToken(merged.OutputTokenDetails.TextTokens, estimated.OutputTokenDetails.TextTokens)
	merged.OutputTokenDetails.AudioTokens = maxToken(merged.OutputTokenDetails.AudioTokens, estimated.OutputTokenDetails.AudioTokens)
	merged.OutputTokenDetails.ImageTokens = maxToken(merged.OutputTokenDetails.ImageTokens, estimated.OutputTokenDetails.ImageTokens)
	merged.OutputTokenDetails.ReasoningTokens = maxToken(merged.OutputTokenDetails.ReasoningTokens, estimated.OutputTokenDetails.ReasoningTokens)
	return normalizeRealtimeUsage(&merged)
}
