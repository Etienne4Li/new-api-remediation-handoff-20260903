package common

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// MaxSecretFileBytes bounds the amount of data read when a secret is supplied
// through a *_FILE environment variable (for example, a Docker secret).  A
// credential is expected to be a short single-line value; refusing an
// unexpectedly large file prevents a configuration mistake from turning into
// an unbounded allocation during process startup.
const MaxSecretFileBytes = 64 * 1024

// MaxDurationSeconds is the largest whole-second value that can be converted
// to a time.Duration without overflowing.  Keep duration conversion in one
// place: multiplying an unchecked environment value by time.Second can wrap
// negative and either disable a timeout or panic a ticker constructor.
const MaxDurationSeconds int64 = math.MaxInt64 / int64(time.Second)

// GetEnvOrFile returns the value of envName, preferring the file named by
// fileEnvName when it is set.  *_FILE support keeps credentials out of process
// arguments and `docker inspect` output while remaining backwards compatible
// with the existing environment-variable configuration.
//
// Secret files are trimmed at the boundaries because Docker/Kubernetes secret
// files commonly end with a newline.  Callers should still validate the
// resulting value according to the credential they are loading.
func GetEnvOrFile(envName, fileEnvName string) (string, error) {
	value := strings.TrimSpace(os.Getenv(envName))
	fileName := strings.TrimSpace(os.Getenv(fileEnvName))
	if fileName == "" {
		return value, nil
	}

	file, err := os.Open(fileName)
	if err != nil {
		return "", fmt.Errorf("read %s from %s=%q: %w", envName, fileEnvName, fileName, err)
	}
	defer file.Close()

	// Read one byte beyond the limit so oversized files are rejected without
	// allocating memory proportional to the file size.
	data, err := io.ReadAll(io.LimitReader(file, MaxSecretFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s from %s=%q: %w", envName, fileEnvName, fileName, err)
	}
	if len(data) > MaxSecretFileBytes {
		return "", fmt.Errorf("%s from %s=%q exceeds %d bytes", envName, fileEnvName, fileName, MaxSecretFileBytes)
	}
	value = strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%s from %s=%q is empty", envName, fileEnvName, fileName)
	}
	return value, nil
}

func GetEnvOrDefault(env string, defaultValue int) int {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %d", env, err.Error(), defaultValue))
		return defaultValue
	}
	return num
}

// GetEnvOrDefaultBounded parses an integer environment variable and accepts it
// only when it falls in the supplied inclusive range. Invalid, missing, and
// out-of-range values all use defaultValue. The helper deliberately preserves
// the existing logging behavior of GetEnvOrDefault for malformed integers.
func GetEnvOrDefaultBounded(env string, defaultValue, minValue, maxValue int) int {
	value := GetEnvOrDefault(env, defaultValue)
	if value < minValue || value > maxValue {
		SysError(fmt.Sprintf(
			"%s must be between %d and %d, using default value: %d",
			env, minValue, maxValue, defaultValue,
		))
		return defaultValue
	}
	return value
}

// DurationFromSeconds converts a signed second count only when the
// multiplication is representable as time.Duration. Callers that require a
// non-negative duration should reject negative values before calling it.
func DurationFromSeconds(seconds int64) (time.Duration, bool) {
	if seconds > MaxDurationSeconds || seconds < -MaxDurationSeconds {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// GetEnvOrDefaultDurationSeconds combines bounded environment parsing with a
// checked seconds-to-duration conversion. minSeconds/maxSeconds are inclusive
// and should express the semantic policy of the caller (for example, whether
// zero means "unlimited").
func GetEnvOrDefaultDurationSeconds(env string, defaultSeconds, minSeconds, maxSeconds int64) time.Duration {
	if minSeconds > maxSeconds {
		return 0
	}
	value := int64(GetEnvOrDefault(env, int(defaultSeconds)))
	if value < minSeconds || value > maxSeconds {
		SysError(fmt.Sprintf(
			"%s must be between %d and %d seconds, using default value: %d",
			env, minSeconds, maxSeconds, defaultSeconds,
		))
		value = defaultSeconds
	}
	if duration, ok := DurationFromSeconds(value); ok {
		return duration
	}
	// A caller-supplied policy should normally make this unreachable, but keep
	// the fallback fail-safe if a future range exceeds time.Duration's limit.
	if duration, ok := DurationFromSeconds(defaultSeconds); ok {
		SysError(fmt.Sprintf("%s duration overflows time.Duration, using default value: %d", env, defaultSeconds))
		return duration
	}
	return 0
}

func GetEnvOrDefaultString(env string, defaultValue string) string {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	return os.Getenv(env)
}

func GetEnvOrDefaultBool(env string, defaultValue bool) bool {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %t", env, err.Error(), defaultValue))
		return defaultValue
	}
	return b
}
