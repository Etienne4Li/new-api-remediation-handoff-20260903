package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetEnvOrDefaultBoundedFallsBackOutsideRange(t *testing.T) {
	t.Setenv("BOUNDED_ENV", "17")
	require.Equal(t, 10, GetEnvOrDefaultBounded("BOUNDED_ENV", 10, 1, 16))

	t.Setenv("BOUNDED_ENV", "0")
	require.Equal(t, 10, GetEnvOrDefaultBounded("BOUNDED_ENV", 10, 1, 16))

	t.Setenv("BOUNDED_ENV", "12")
	require.Equal(t, 12, GetEnvOrDefaultBounded("BOUNDED_ENV", 10, 1, 16))
}

func TestDurationFromSecondsRejectsOverflow(t *testing.T) {
	duration, ok := DurationFromSeconds(MaxDurationSeconds)
	require.True(t, ok)
	require.Equal(t, time.Duration(MaxDurationSeconds)*time.Second, duration)

	_, ok = DurationFromSeconds(MaxDurationSeconds + 1)
	require.False(t, ok)
	_, ok = DurationFromSeconds(-MaxDurationSeconds - 1)
	require.False(t, ok)
}

func TestGetEnvOrDefaultDurationSecondsBoundsZeroAndLargeValues(t *testing.T) {
	t.Setenv("DURATION_ENV", "0")
	require.Equal(t, time.Duration(0), GetEnvOrDefaultDurationSeconds("DURATION_ENV", 5, 0, 60))

	t.Setenv("DURATION_ENV", "-1")
	require.Equal(t, 5*time.Second, GetEnvOrDefaultDurationSeconds("DURATION_ENV", 5, 0, 60))

	t.Setenv("DURATION_ENV", "61")
	require.Equal(t, 5*time.Second, GetEnvOrDefaultDurationSeconds("DURATION_ENV", 5, 0, 60))
}

func TestGetEnvOrFilePrefersSecretFileAndTrimsTrailingNewline(t *testing.T) {
	t.Setenv("TEST_SECRET", "environment-value")
	file := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(file, []byte("file-value\n"), 0600))
	t.Setenv("TEST_SECRET_FILE", file)

	value, err := GetEnvOrFile("TEST_SECRET", "TEST_SECRET_FILE")
	require.NoError(t, err)
	require.Equal(t, "file-value", value)
}

func TestGetEnvOrFileRejectsEmptyAndOversizedSecretFiles(t *testing.T) {
	emptyFile := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(emptyFile, []byte("\n"), 0600))
	t.Setenv("TEST_SECRET_FILE", emptyFile)
	_, err := GetEnvOrFile("TEST_SECRET", "TEST_SECRET_FILE")
	require.Error(t, err)
	require.ErrorContains(t, err, "is empty")

	largeFile := filepath.Join(t.TempDir(), "large")
	require.NoError(t, os.WriteFile(largeFile, []byte(strings.Repeat("x", MaxSecretFileBytes+1)), 0600))
	t.Setenv("TEST_SECRET_FILE", largeFile)
	_, err = GetEnvOrFile("TEST_SECRET", "TEST_SECRET_FILE")
	require.Error(t, err)
	require.ErrorContains(t, err, "exceeds")
}

func TestGetEnvOrFileFallsBackToEnvironment(t *testing.T) {
	t.Setenv("TEST_SECRET", "environment-value")
	t.Setenv("TEST_SECRET_FILE", "")

	value, err := GetEnvOrFile("TEST_SECRET", "TEST_SECRET_FILE")
	require.NoError(t, err)
	require.Equal(t, "environment-value", value)
}
