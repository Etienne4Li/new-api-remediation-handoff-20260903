package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// If a rotation worker loses the setup mutex (for example while the startup
// logger is still being installed), it must release the in-flight marker.  A
// stale marker permanently suppresses every later rotation after the first
// million log records.
func TestSetupLoggerContentionReleasesRotationMarker(t *testing.T) {
	previousLogDir := *common.LogDir
	*common.LogDir = t.TempDir()
	t.Cleanup(func() { *common.LogDir = previousLogDir })

	logStateMu.Lock()
	previousWorking := setupLogWorking
	previousCount := logCount
	previousBytes := logBytes
	setupLogWorking = true
	logCount = 0
	logBytes = 0
	logStateMu.Unlock()
	t.Cleanup(func() {
		logStateMu.Lock()
		setupLogWorking = previousWorking
		logCount = previousCount
		logBytes = previousBytes
		logStateMu.Unlock()
	})

	// Hold the setup mutex so SetupLogger takes its contention path.
	setupLogLock.Lock()
	SetupLogger()
	setupLogLock.Unlock()

	logStateMu.Lock()
	working := setupLogWorking
	logStateMu.Unlock()
	require.False(t, working, "a failed rotation attempt must not leave setupLogWorking stuck")
}

func TestPruneLogFilesHonorsAgeAndCountWithoutTouchingUnrelatedFiles(t *testing.T) {
	previousDir := *common.LogDir
	*common.LogDir = t.TempDir()
	t.Cleanup(func() { *common.LogDir = previousDir })

	previousConfig := getRotationConfig()
	t.Cleanup(func() {
		rotationConfigMu.Lock()
		currentRotationConfig = previousConfig
		rotationConfigMu.Unlock()
	})
	rotationConfigMu.Lock()
	currentRotationConfig = rotationConfig{
		maxBytes: 1024,
		maxFiles: 2,
		maxAge:   24 * time.Hour,
	}
	rotationConfigMu.Unlock()

	now := time.Now()
	writeLog := func(name string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(*common.LogDir, name)
		require.NoError(t, os.WriteFile(path, []byte("log"), 0600))
		stamp := now.Add(-age)
		require.NoError(t, os.Chtimes(path, stamp, stamp))
		return path
	}
	current := writeLog("oneapi-current.log", 2*time.Hour)
	writeLog("oneapi-recent.log", 3*time.Hour)
	writeLog("oneapi-old.log", 48*time.Hour)
	unrelated := filepath.Join(*common.LogDir, "operator-notes.log")
	require.NoError(t, os.WriteFile(unrelated, []byte("keep"), 0600))

	pruneLogFiles(current)

	_, err := os.Stat(current)
	require.NoError(t, err, "the active log file must be retained")
	_, err = os.Stat(filepath.Join(*common.LogDir, "oneapi-recent.log"))
	require.NoError(t, err, "a recent file within the count limit must be retained")
	_, err = os.Stat(filepath.Join(*common.LogDir, "oneapi-old.log"))
	require.ErrorIs(t, err, os.ErrNotExist, "expired files must be removed")
	_, err = os.Stat(unrelated)
	require.NoError(t, err, "retention must not remove unrelated operator files")
}
