package openai

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestAddRealtimeTokenCountsSaturatesAndDropsNegativeValues(t *testing.T) {
	usage := &dto.RealtimeUsage{}
	addRealtimeTokenCounts(usage, math.MaxInt, 1, true)
	addRealtimeTokenCounts(usage, -10, 4, true)

	require.Equal(t, math.MaxInt, usage.TotalTokens)
	require.Equal(t, math.MaxInt, usage.InputTokens)
	require.Equal(t, math.MaxInt, usage.InputTokenDetails.TextTokens)
	require.Equal(t, 5, usage.InputTokenDetails.AudioTokens)
}

func TestAddRealtimeUsageSaturatesEveryBillingDimension(t *testing.T) {
	dst := &dto.RealtimeUsage{
		TotalTokens: math.MaxInt - 1,
		InputTokens: math.MaxInt - 1,
	}
	src := &dto.RealtimeUsage{
		TotalTokens:  math.MaxInt,
		InputTokens:  math.MaxInt,
		OutputTokens: -1,
		InputTokenDetails: dto.InputTokenDetails{
			CachedTokens:         math.MaxInt,
			TextTokens:           3,
			AudioTokens:          -2,
			ImageTokens:          math.MaxInt,
			CachedCreationTokens: 4,
			CacheWriteTokens:     math.MaxInt,
		},
		OutputTokenDetails: dto.OutputTokenDetails{
			TextTokens:      math.MaxInt,
			AudioTokens:     5,
			ImageTokens:     -3,
			ReasoningTokens: 7,
		},
	}

	addRealtimeUsage(dst, src)

	require.Equal(t, math.MaxInt, dst.TotalTokens)
	require.Equal(t, math.MaxInt, dst.InputTokens)
	require.Equal(t, math.MaxInt, dst.OutputTokens)
	require.Equal(t, math.MaxInt, dst.InputTokenDetails.CachedTokens)
	require.Equal(t, 3, dst.InputTokenDetails.TextTokens)
	require.Equal(t, 0, dst.InputTokenDetails.AudioTokens)
	require.Equal(t, math.MaxInt, dst.InputTokenDetails.ImageTokens)
	require.Equal(t, 4, dst.InputTokenDetails.CachedCreationTokens)
	require.Equal(t, math.MaxInt, dst.InputTokenDetails.CacheWriteTokens)
	require.Equal(t, math.MaxInt, dst.OutputTokenDetails.TextTokens)
	require.Equal(t, 5, dst.OutputTokenDetails.AudioTokens)
	require.Equal(t, 0, dst.OutputTokenDetails.ImageTokens)
	require.Equal(t, 7, dst.OutputTokenDetails.ReasoningTokens)
}

func TestRealtimeUsageAccumulatorSerializesConcurrentReaders(t *testing.T) {
	accumulator := &realtimeUsageAccumulator{}
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)

	go func() {
		defer workers.Done()
		<-start
		accumulator.addTokenCounts(2, 3, true)
	}()
	go func() {
		defer workers.Done()
		<-start
		accumulator.addTokenCounts(5, 7, false)
	}()

	close(start)
	workers.Wait()

	usage, ok := accumulator.take()
	require.True(t, ok)
	require.Equal(t, 17, usage.TotalTokens)
	require.Equal(t, 5, usage.InputTokens)
	require.Equal(t, 12, usage.OutputTokens)
	require.Equal(t, 2, usage.InputTokenDetails.TextTokens)
	require.Equal(t, 3, usage.InputTokenDetails.AudioTokens)
	require.Equal(t, 5, usage.OutputTokenDetails.TextTokens)
	require.Equal(t, 7, usage.OutputTokenDetails.AudioTokens)

	_, ok = accumulator.take()
	require.False(t, ok)
}

func TestRealtimeResponseUsageHandlesMissingResponse(t *testing.T) {
	require.Nil(t, realtimeResponseUsage(nil))
	require.Nil(t, realtimeResponseUsage(&dto.RealtimeEvent{Type: dto.RealtimeEventTypeResponseDone}))
	require.Nil(t, realtimeResponseUsage(&dto.RealtimeEvent{
		Type:     dto.RealtimeEventTypeResponseDone,
		Response: &dto.RealtimeResponse{},
	}))
	require.Nil(t, realtimeResponseUsage(&dto.RealtimeEvent{
		Type: dto.RealtimeEventTypeResponseDone,
		Response: &dto.RealtimeResponse{
			Usage: &dto.RealtimeUsage{},
		},
	}))

	usage := &dto.RealtimeUsage{InputTokens: 4}
	event := &dto.RealtimeEvent{
		Type: dto.RealtimeEventTypeResponseDone,
		Response: &dto.RealtimeResponse{
			Usage: usage,
		},
	}
	require.Same(t, usage, realtimeResponseUsage(event))
}

func TestRealtimeResponseUsageAcceptsDetailsWithoutTotal(t *testing.T) {
	usage := &dto.RealtimeUsage{
		InputTokenDetails: dto.InputTokenDetails{AudioTokens: 4},
	}
	event := &dto.RealtimeEvent{
		Type: dto.RealtimeEventTypeResponseDone,
		Response: &dto.RealtimeResponse{
			Usage: usage,
		},
	}
	require.Same(t, usage, realtimeResponseUsage(event))
}

func TestRealtimeUsageAccumulatorKeepsDimensionsWhenTotalIsZero(t *testing.T) {
	accumulator := &realtimeUsageAccumulator{}
	accumulator.addUsage(&dto.RealtimeUsage{
		InputTokenDetails: dto.InputTokenDetails{TextTokens: 6},
	})

	usage, ok := accumulator.take()
	require.True(t, ok)
	require.Equal(t, 6, usage.TotalTokens)
	require.Equal(t, 6, usage.InputTokenDetails.TextTokens)
}

func TestRealtimeUsageAccumulatorRotatePreservesNextResponseUsage(t *testing.T) {
	accumulator := &realtimeUsageAccumulator{}
	accumulator.addTokenCounts(3, 0, false)

	first, ok := accumulator.rotate()
	require.True(t, ok)
	require.Equal(t, 3, first.OutputTokens)

	// Events arriving after response.done belong to the next response. A
	// response-boundary operation must not discard them while replacing the
	// provider usage for the completed response.
	accumulator.addTokenCounts(5, 0, true)
	next, ok := accumulator.take()
	require.True(t, ok)
	require.Equal(t, 5, next.InputTokens)
}

func TestMergeRealtimeUsageWithEstimateFillsMissingProviderDimensions(t *testing.T) {
	provider := &dto.RealtimeUsage{
		InputTokens:       10,
		OutputTokens:      0,
		TotalTokens:       10,
		InputTokenDetails: dto.InputTokenDetails{TextTokens: 10},
	}
	estimate := &dto.RealtimeUsage{
		InputTokens:        4,
		OutputTokens:       7,
		TotalTokens:        11,
		OutputTokenDetails: dto.OutputTokenDetails{AudioTokens: 7},
	}

	merged := mergeRealtimeUsageWithEstimate(provider, estimate)
	require.Equal(t, 10, merged.InputTokens)
	require.Equal(t, 7, merged.OutputTokens)
	require.Equal(t, 17, merged.TotalTokens)
	require.Equal(t, 7, merged.OutputTokenDetails.AudioTokens)
}

func TestRealtimeResponseTrackerDeduplicatesByResponseOrEventID(t *testing.T) {
	tracker := &realtimeResponseTracker{}
	first := &dto.RealtimeEvent{
		EventId:  "evt-1",
		Response: &dto.RealtimeResponse{Id: "resp-1"},
	}
	duplicateWithNewEventID := &dto.RealtimeEvent{
		EventId:  "evt-2",
		Response: &dto.RealtimeResponse{Id: "resp-1"},
	}
	second := &dto.RealtimeEvent{EventId: "evt-3", Response: &dto.RealtimeResponse{Id: "resp-2"}}

	require.True(t, tracker.accept(first))
	require.False(t, tracker.accept(duplicateWithNewEventID))
	require.True(t, tracker.accept(second))
}

func TestNormalizeRealtimeUsageClampsNegativeFields(t *testing.T) {
	normalized := normalizeRealtimeUsage(&dto.RealtimeUsage{
		TotalTokens:  -1,
		InputTokens:  -2,
		OutputTokens: -3,
		InputTokenDetails: dto.InputTokenDetails{
			TextTokens:  4,
			AudioTokens: -5,
		},
		OutputTokenDetails: dto.OutputTokenDetails{AudioTokens: -6},
	})

	require.Equal(t, 4, normalized.TotalTokens)
	require.Equal(t, 4, normalized.InputTokens)
	require.Zero(t, normalized.OutputTokens)
	require.Zero(t, normalized.InputTokenDetails.AudioTokens)
	require.Zero(t, normalized.OutputTokenDetails.AudioTokens)
}

func TestOpenaiRealtimeHandlerRejectsNilContextOrConnections(t *testing.T) {
	err, usage := OpenaiRealtimeHandler(nil, nil)
	require.Error(t, err)
	require.Nil(t, usage)
}

// A malformed frame from the upstream must retain error semantics all the way
// through OpenaiRealtimeHandler. The handler still returns the usage snapshot
// so the websocket relay can settle any frames observed before the failure.
func TestOpenaiRealtimeHandlerPropagatesUpstreamDecodeError(t *testing.T) {
	clientServer, _ := newRealtimeTestWebsocketPair(t)
	targetServer, targetPeer := newRealtimeTestWebsocketPair(t)

	targetPeer.WriteMessage(websocket.TextMessage, []byte("{"))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/realtime", nil).WithContext(ctx)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req
	info := &relaycommon.RelayInfo{
		ClientWs:        clientServer,
		TargetWs:        targetServer,
		OriginModelName: "gpt-4o-realtime-preview",
	}

	err, usage := OpenaiRealtimeHandler(ginCtx, info)
	require.Error(t, err)
	require.NotNil(t, usage)
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.True(t, types.IsSkipRetryError(err))
}

// newRealtimeTestWebsocketPair returns the server-side and client-side ends
// of a local Gorilla websocket connection. The server handler may return after
// hijacking; the connection remains owned by the test until cleanup.
func newRealtimeTestWebsocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	peer, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	serverConn := <-accepted
	t.Cleanup(func() {
		_ = peer.Close()
		_ = serverConn.Close()
	})
	return serverConn, peer
}

func TestRealtimeCountModelNameFallsBackBeforeChannelMetaInitialization(t *testing.T) {
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-4o"}
	require.Equal(t, "gpt-4o", realtimeCountModelName(info))

	info.ChannelMeta = &relaycommon.ChannelMeta{UpstreamModelName: "mapped-model"}
	require.Equal(t, "mapped-model", realtimeCountModelName(info))
}
