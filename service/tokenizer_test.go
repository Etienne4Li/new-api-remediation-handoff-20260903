package service

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountTextTokenLazilyInitializesUnknownOpenAIModel(t *testing.T) {
	// Relay adapters can be exercised without booting main.go. An unknown
	// OpenAI-compatible model must still use the default codec instead of
	// passing a nil encoder to tokenizer.Codec.Count.
	got := CountTextToken("a short response", "gpt-unreleased-test-model")
	assert.Positive(t, got)

	// InitTokenEncoders is intentionally idempotent; calling it after the lazy
	// path must not replace the published encoder or panic.
	InitTokenEncoders()
	InitTokenEncoders()
	assert.NotNil(t, getTokenEncoder("gpt-unreleased-test-model"))
}

func TestCountTextTokenConcurrentLazyInitialization(t *testing.T) {
	const workers = 32
	results := make(chan int, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			results <- CountTextToken("concurrent response", "gpt-concurrent-unknown-model")
		}()
	}
	wg.Wait()
	close(results)

	for got := range results {
		require.Positive(t, got)
	}
}
