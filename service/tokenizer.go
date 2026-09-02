package service

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/tiktoken-go/tokenizer"
	"github.com/tiktoken-go/tokenizer/codec"
)

// tokenEncoderMap won't grow after initialization
var defaultTokenEncoder tokenizer.Codec

// tokenEncoderOnce makes the default encoder safe to use from package-level
// callers (including unit tests) that do not run main.go's eager
// initialization. The encoder is immutable after construction, so a one-time
// initialization also avoids a data race between the request path and
// InitTokenEncoders.
var tokenEncoderOnce sync.Once

// tokenEncoderMap is used to store token encoders for different models
var tokenEncoderMap = make(map[string]tokenizer.Codec)

// tokenEncoderMutex protects tokenEncoderMap for concurrent access
var tokenEncoderMutex sync.RWMutex

func InitTokenEncoders() {
	tokenEncoderOnce.Do(func() {
		common.SysLog("initializing token encoders")
		// Keep the assignment under the same mutex used by getTokenEncoder. This
		// is mostly defensive (sync.Once publishes the value to callers), and it
		// also keeps tests that inspect the cache race-free.
		tokenEncoderMutex.Lock()
		defaultTokenEncoder = codec.NewCl100kBase()
		tokenEncoderMutex.Unlock()
		common.SysLog("token encoders initialized")
	})
}

func getTokenEncoder(model string) tokenizer.Codec {
	// Production starts this from main.go, but relay packages and tests can call
	// CountTextToken directly. Lazily initialize so those paths never pass a
	// nil codec to getTokenNum.
	InitTokenEncoders()

	// First, try to get the encoder from cache with read lock
	tokenEncoderMutex.RLock()
	if encoder, exists := tokenEncoderMap[model]; exists {
		tokenEncoderMutex.RUnlock()
		return encoder
	}
	tokenEncoderMutex.RUnlock()

	// If not in cache, create new encoder with write lock
	tokenEncoderMutex.Lock()
	defer tokenEncoderMutex.Unlock()

	// Double-check if another goroutine already created the encoder
	if encoder, exists := tokenEncoderMap[model]; exists {
		return encoder
	}

	// Create new encoder
	modelCodec, err := tokenizer.ForModel(tokenizer.Model(model))
	if err != nil || modelCodec == nil {
		// Cache the default encoder for this model to avoid repeated failures
		tokenEncoderMap[model] = defaultTokenEncoder
		return defaultTokenEncoder
	}

	// Cache the new encoder
	tokenEncoderMap[model] = modelCodec
	return modelCodec
}

func getTokenNum(tokenEncoder tokenizer.Codec, text string) int {
	if text == "" {
		return 0
	}
	tkm, _ := tokenEncoder.Count(text)
	return tkm
}
