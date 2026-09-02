package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a single blocked write to a slow client so the
	// unconditional wg.Wait() in cleanup can always finish. Without it, a slow
	// but connected client (full TCP buffer, no server WriteTimeout) could hang
	// the handler forever.
	streamWriteTimeout                   = 30 * time.Second
	defaultStreamingTimeout              = 5 * time.Minute
	defaultClientDisconnectDrainTimeout  = 1200 * time.Millisecond
	defaultClientDisconnectDrainMaxBytes = 4 << 20
)

// StreamScannerDrainOptions enables a short, bounded read of an upstream SSE
// body after the downstream client disconnects.  During the drain the normal
// dataHandler is still invoked, but it must avoid writing to c.Writer when the
// request context is done.  This is intended for protocol adapters (notably
// OpenAI Responses) that can extract authoritative usage from late terminal
// events.  A nil options pointer preserves the historical immediate-close
// behaviour.
type StreamScannerDrainOptions struct {
	Timeout  time.Duration
	MaxBytes int64
}

// StreamScannerOptions configures provider-specific line framing while keeping
// cancellation, timeout, ping, body-close, and goroutine ownership in the
// shared stream engine. AcceptRawJSON is intended for providers whose default
// response is newline-delimited JSON but which switch to SSE when requested.
type StreamScannerOptions struct {
	Drain         *StreamScannerDrainOptions
	AcceptRawJSON bool
	// ParseSSEFrames aggregates consecutive data lines until the SSE record
	// boundary (an empty line) and passes the associated event name to
	// EventHandler. It is opt-in so legacy providers that emit one data line
	// per record without a blank separator keep their historical behavior.
	ParseSSEFrames bool
	EventHandler   func(event string, data string, sr *StreamResult)
}

func (o *StreamScannerDrainOptions) normalized() (time.Duration, int64) {
	if o == nil {
		return 0, 0
	}
	timeout := o.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = defaultClientDisconnectDrainTimeout
	}
	maxBytes := o.MaxBytes
	if maxBytes <= 0 || maxBytes > 64<<20 {
		maxBytes = defaultClientDisconnectDrainMaxBytes
	}
	return timeout, maxBytes
}

func streamingTimeoutDuration() time.Duration {
	seconds := int64(constant.StreamingTimeout)
	if seconds <= 0 || seconds > int64((time.Duration(1<<63-1))/time.Second) {
		// A zero/negative value can be produced by an unset or malformed
		// environment override. time.NewTicker panics for non-positive
		// durations, while an overflowing multiplication can wrap negative;
		// fail safe to the documented 300-second default in both cases.
		return defaultStreamingTimeout
	}
	return time.Duration(seconds) * time.Second
}

func getScannerBufferSize() int {
	maxMB := int64(constant.StreamScannerMaxBufferMB)
	maxInt := int64(^uint(0) >> 1)
	if maxMB > 0 && maxMB <= maxInt>>20 {
		return int(maxMB << 20)
	}
	return DefaultMaxScannerBufferSize
}

func NewStreamScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, InitialScannerBufferSize), getScannerBufferSize())
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// ExtendWriteDeadline pushes the connection write deadline forward before each
// stream write. Best-effort: writers that don't support deadlines (e.g.
// httptest recorders) are silently ignored.
func ExtendWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) {
	streamScannerHandler(c, resp, info, dataHandler, nil)
}

// StreamScannerHandlerWithDrain is the opt-in variant used by adapters that
// can safely process late upstream usage events after a client disconnect.
// The body is still closed as soon as the bounded drain expires (or reaches
// MaxBytes), so a provider cannot keep generating indefinitely for an abandoned
// request.
func StreamScannerHandlerWithDrain(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult), options *StreamScannerDrainOptions) {
	streamScannerHandler(c, resp, info, dataHandler, &StreamScannerOptions{Drain: options})
}

// StreamScannerHandlerWithOptions is the protocol-aware entry point for the
// few providers that need framing beyond conventional SSE data lines.
func StreamScannerHandlerWithOptions(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult), options *StreamScannerOptions) {
	streamScannerHandler(c, resp, info, dataHandler, options)
}

// StreamScannerHandlerWithEvents parses standard SSE event/data records and
// exposes the event name alongside the payload. It also accepts compact raw
// JSON lines for providers that use newline-delimited JSON when SSE is not
// requested. The same lifecycle guarantees as StreamScannerHandler apply.
func StreamScannerHandlerWithEvents(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, eventHandler func(event string, data string, sr *StreamResult)) {
	streamScannerHandler(c, resp, info, nil, &StreamScannerOptions{
		ParseSSEFrames: true,
		AcceptRawJSON:  true,
		EventHandler:   eventHandler,
	})
}

// StreamScannerHandlerWithEventsAndDrain is the bounded-drain variant of
// StreamScannerHandlerWithEvents. Adapters may continue parsing authoritative
// terminal usage after a downstream disconnect while StreamResult.IsDraining
// tells them to skip writes to the closed client.
func StreamScannerHandlerWithEventsAndDrain(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, eventHandler func(event string, data string, sr *StreamResult), options *StreamScannerDrainOptions) {
	streamScannerHandler(c, resp, info, nil, &StreamScannerOptions{
		Drain:          options,
		ParseSSEFrames: true,
		AcceptRawJSON:  true,
		EventHandler:   eventHandler,
	})
}

func streamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult), options *StreamScannerOptions) {

	if resp == nil || (dataHandler == nil && (options == nil || options.EventHandler == nil)) {
		return
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	ctx, cancel := context.WithCancel(context.Background())

	streamingTimeout := streamingTimeoutDuration()

	var (
		stopChan    = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner     = NewStreamScanner(resp.Body)
		ticker      = time.NewTicker(streamingTimeout)
		pingTicker  *time.Ticker
		writeMutex  sync.Mutex     // Mutex to protect concurrent writes
		wg          sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce sync.Once
		stopOnce    sync.Once
		drainDone   = make(chan struct{})
	)
	var (
		drainActive    atomic.Bool
		drainBytes     atomic.Int64
		drainLimit     = make(chan struct{})
		drainLimitOnce sync.Once
	)
	var drainOptions *StreamScannerDrainOptions
	if options != nil {
		drainOptions = options.Drain
	}
	drainTimeout, drainMaxBytes := drainOptions.normalized()
	requestDone := (<-chan struct{})(nil)
	if c != nil && c.Request != nil {
		requestDone = c.Request.Context().Done()
	}

	stop := func() {
		// During an opt-in client-disconnect drain, goroutine defers must not
		// close stopChan: the scanner is intentionally kept alive to consume the
		// bounded upstream tail.  cleanup uses forceStop after the drain window.
		if drainOptions != nil && (drainActive.Load() || (c != nil && c.Request != nil && c.Request.Context().Err() != nil)) {
			return
		}
		stopOnce.Do(func() {
			close(stopChan)
		})
	}
	forceStop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSettingSnapshot()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	isDraining := func() bool {
		if drainActive.Load() {
			return true
		}
		return c != nil && c.Request != nil && c.Request.Context().Err() != nil && drainOptions != nil
	}

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			forceStop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}

			wg.Wait()
			drainActive.Store(false)
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)
	copyCodexSSEHeaders(c, resp)
	SetEventStreamHeaders(c)

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, fmt.Sprintf("ping goroutine panic: panic_meta=%s", common.SensitiveLogMeta(fmt.Sprint(r))))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			for {
				select {
				case <-pingTicker.C:
					var err error
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						ExtendWriteDeadline(c)
						err = PingData(c)
					}()
					if err != nil {
						logger.LogError(c, "ping data error: error_meta="+common.SensitiveLogMeta(err.Error()))
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-pingTimeout.C:
					logger.LogError(c, "ping goroutine max duration reached")
					return
				}
			}
		})
	}

	type streamChunk struct {
		event string
		data  string
	}
	dataChan := make(chan streamChunk, 10)

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("data handler goroutine panic: panic_meta=%s", common.SensitiveLogMeta(fmt.Sprint(r))))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		defer close(drainDone)
		sr := newStreamResult(info.StreamStatus, isDraining)
		for chunk := range dataChan {
			sr.reset()
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				ExtendWriteDeadline(c)
				if options != nil && options.EventHandler != nil {
					options.EventHandler(chunk.event, chunk.data, sr)
				} else {
					dataHandler(chunk.data, sr)
				}
			}()
			if sr.IsStopped() {
				return
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("scanner goroutine panic: panic_meta=%s", common.SensitiveLogMeta(fmt.Sprint(r))))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			stop()
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		dispatch := func(event, data string) bool {
			data = strings.TrimSpace(data)
			if data == "" {
				return true
			}
			if data == "[DONE]" {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return false
			}
			if strings.HasPrefix(data, "[DONE]") {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return false
			}
			if drainActive.Load() && drainMaxBytes > 0 {
				if drainBytes.Add(int64(len(data))) > drainMaxBytes {
					drainLimitOnce.Do(func() { close(drainLimit) })
					return false
				}
			}
			if info != nil {
				info.SetFirstResponseTime()
				info.ReceivedResponseCount++
			}
			select {
			case dataChan <- streamChunk{event: event, data: data}:
				return true
			case <-ctx.Done():
				return false
			case <-stopChan:
				return false
			}
		}

		if options != nil && options.ParseSSEFrames {
			var (
				eventName string
				dataLines []string
				hasData   bool
			)
			flushFrame := func() bool {
				if !hasData {
					eventName = ""
					return true
				}
				data := strings.Join(dataLines, "\n")
				event := eventName
				eventName = ""
				dataLines = nil
				hasData = false
				return dispatch(event, data)
			}
			for scanner.Scan() {
				select {
				case <-stopChan:
					return
				case <-ctx.Done():
					return
				default:
				}

				ticker.Reset(streamingTimeout)
				line := strings.TrimSuffix(scanner.Text(), "\r")
				logger.LogDebug(c, "stream scanner data: data_meta=%s", common.SensitiveLogBody([]byte(line)))
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					if !flushFrame() {
						return
					}
					continue
				}
				if strings.HasPrefix(trimmed, "event:") {
					// A missing blank separator is malformed SSE, but flushing the
					// previous record preserves ordering and avoids joining two JSON
					// objects into an unparsable payload.
					if hasData && !flushFrame() {
						return
					}
					eventName = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
					continue
				}
				if strings.HasPrefix(trimmed, "data:") {
					value := strings.TrimPrefix(trimmed, "data:")
					if strings.HasPrefix(value, " ") {
						value = value[1:]
					}
					dataLines = append(dataLines, value)
					hasData = true
					continue
				}
				if strings.HasPrefix(trimmed, ":") || strings.HasPrefix(trimmed, "id:") || strings.HasPrefix(trimmed, "retry:") {
					continue
				}
				if options.AcceptRawJSON && strings.HasPrefix(trimmed, "{") {
					if hasData && !flushFrame() {
						return
					}
					if !dispatch("", trimmed) {
						return
					}
				}
			}
			if !flushFrame() {
				return
			}
		} else {
			for scanner.Scan() {
				// 检查是否需要停止
				select {
				case <-stopChan:
					return
				case <-ctx.Done():
					return
				default:
				}

				ticker.Reset(streamingTimeout)
				data := scanner.Text()
				// SSE data is often the model's prompt/response and may contain
				// credentials in tool arguments. Keep debug correlation without
				// persisting the payload itself.
				logger.LogDebug(c, "stream scanner data: data_meta=%s", common.SensitiveLogBody([]byte(data)))

				// Accept both the usual SSE form (`data: [DONE]`) and the bare
				// sentinel emitted by a few compatible providers.  Slice only after
				// confirming the `data:` prefix; unconditionally dropping five bytes
				// turns a bare `[DONE]` into `]` and lets the scanner consume data that
				// follows a terminal marker.
				var eventData string
				if strings.TrimSpace(data) == "[DONE]" {
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
					logger.LogDebug(c, "received bare [DONE], stopping scanner")
					return
				}
				if strings.HasPrefix(data, "data:") {
					data = strings.TrimSpace(strings.TrimPrefix(data, "data:"))
				} else if options != nil && options.AcceptRawJSON && strings.HasPrefix(strings.TrimSpace(data), "{") {
					data = strings.TrimSpace(data)
				} else {
					continue
				}
				if data == "" {
					continue
				}
				if !strings.HasPrefix(data, "[DONE]") {
					eventData = data
				}
				if eventData == "" {
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
					logger.LogDebug(c, "received [DONE], stopping scanner")
					return
				}
				if !dispatch("", eventData) {
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, "scanner error: error_meta="+common.SensitiveLogMeta(err.Error()))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// 主循环等待完成或超时
	select {
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
	case <-stopChan:
		// EndReason already set by the goroutine that triggered stopChan
	case <-requestDone:
		// 客户端断开：立即 cleanup 关闭上游 resp.Body，解除 scanner 阻塞并让上游停止生成，
		// 避免为已放弃的请求继续消费上游 token。协议适配器可选择先进行
		// 一个有上限的 drain，以取得已经在途的最终 usage。
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
	}
	// stopChan may win the select because the ping/data goroutine observed the
	// same cancellation first. Re-check the request context after either branch
	// so the drain contract is deterministic rather than scheduler-dependent.
	if drainOptions != nil && c != nil && c.Request != nil && c.Request.Context().Err() != nil &&
		info.StreamStatus.EndReason != relaycommon.StreamEndReasonDone &&
		info.StreamStatus.EndReason != relaycommon.StreamEndReasonEOF {
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
		drainActive.Store(true)
		timer := time.NewTimer(drainTimeout)
		select {
		case <-drainDone:
		case <-drainLimit:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	cleanup()
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, fmt.Sprintf("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}
}
