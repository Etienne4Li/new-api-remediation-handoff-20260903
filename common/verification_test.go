package common

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetVerificationStateForTest(t *testing.T) {
	t.Helper()
	verificationMutex.Lock()
	verificationMap = make(map[string]verificationValue)
	verificationMutex.Unlock()
}

func TestConsumeVerificationCodeIsSingleUse(t *testing.T) {
	resetVerificationStateForTest(t)
	RegisterVerificationCodeWithKey("reset@example.com", "one-time", PasswordResetPurpose)

	assert.True(t, ConsumeVerificationCodeWithKey("reset@example.com", "one-time", PasswordResetPurpose))
	assert.False(t, ConsumeVerificationCodeWithKey("reset@example.com", "one-time", PasswordResetPurpose))
	assert.False(t, VerifyCodeWithKey("reset@example.com", "one-time", PasswordResetPurpose))
}

func TestConsumeVerificationCodeAllowsRetryAfterWrongCode(t *testing.T) {
	resetVerificationStateForTest(t)
	RegisterVerificationCodeWithKey("retry@example.com", "expected", PasswordResetPurpose)

	assert.False(t, ConsumeVerificationCodeWithKey("retry@example.com", "wrong", PasswordResetPurpose))
	assert.True(t, ConsumeVerificationCodeWithKey("retry@example.com", "expected", PasswordResetPurpose))
}

func TestConsumeVerificationCodeConcurrentRedeemOnlySucceedsOnce(t *testing.T) {
	resetVerificationStateForTest(t)
	RegisterVerificationCodeWithKey("race@example.com", "concurrent", PasswordResetPurpose)

	const attempts = 16
	results := make(chan bool, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- ConsumeVerificationCodeWithKey("race@example.com", "concurrent", PasswordResetPurpose)
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result {
			successes++
		}
	}
	require.Equal(t, 1, successes)
}
