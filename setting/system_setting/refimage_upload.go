package system_setting

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
)

// The reference-image upload bridge turns multipart reference images attached
// to a task-plugin request into public HTTPS URLs hosted by an external image
// host, then rewrites the plugin request fields so the plugin only ever sees
// URLs. It mirrors the startup-only configuration style of
// setting/system_setting/task_artifact_store.go.
//
// Credential injection (SecretRef convention): the upload credential is read
// from the process environment, either directly from REFIMAGE_UPLOAD_TOKEN or
// from the file named by REFIMAGE_UPLOAD_TOKEN_FILE. It is never compiled into
// the binary, never accepted from a request, a database option, or a plugin
// manifest, and never written to logs.
const (
	// RefImageUploadPluginKey is the only task plugin key the bridge serves.
	RefImageUploadPluginKey = "lietio-video"

	RefImageUploadEnabledEnv        = "REFIMAGE_UPLOAD_ENABLED"
	RefImageUploadEndpointEnv       = "REFIMAGE_UPLOAD_ENDPOINT"
	RefImageUploadTokenEnv          = "REFIMAGE_UPLOAD_TOKEN"
	RefImageUploadTokenFileEnv      = "REFIMAGE_UPLOAD_TOKEN_FILE"
	RefImageUploadTimeoutSecondsEnv = "REFIMAGE_UPLOAD_TIMEOUT_SECONDS"
)

// DefaultRefImageUploadEndpoint targets the image host on the shared container
// network by its service name. It deliberately does NOT use
// http://host.docker.internal:13022, which resolves through the docker host
// loopback and is unreachable from inside the new-api container when Zipline
// only publishes 127.0.0.1:13022 on the host.
//
// Attaching both containers to one user-defined network and addressing Zipline
// by service name is a deploy-time action; see
// docs/development/refimage-upload.md and /home/debian/handoff/REVIEW-refimage-upload.md.
// Deployments that choose a different shared network or alias must set
// REFIMAGE_UPLOAD_ENDPOINT explicitly.
const DefaultRefImageUploadEndpoint = "http://zipline:3000/api/upload"

// RefImageUploadPublicPrefix is the only URL prefix the host accepts in an
// upload response and the only prefix it will hand to the plugin.
const RefImageUploadPublicPrefix = "https://z.lietio.com/"

// RefImageUploadDeletesAt is sent as the x-zipline-deletes-at request header so
// hosted reference images expire after 24 hours. Uploaded objects are never
// deleted by this process: before submission an object cannot be associated
// with a task id, so retention is left to the host-side expiry.
const RefImageUploadDeletesAt = "24h"

const (
	DefaultRefImageUploadTimeoutSeconds = 30
	MaxRefImageUploadTimeoutSeconds     = 120
	MaxRefImageUploadTokenBytes         = 4096
	MaxRefImageUploadTokenPathBytes     = 512
)

// RefImageUploadConfig is the resolved startup configuration for the bridge.
type RefImageUploadConfig struct {
	Enabled        bool
	Endpoint       string
	TimeoutSeconds int

	token       string
	tokenSource string
}

// PublicPrefix is the fixed public URL prefix accepted from the image host.
func (c RefImageUploadConfig) PublicPrefix() string { return RefImageUploadPublicPrefix }

// Token returns the upload credential. Callers must never log or persist it.
func (c RefImageUploadConfig) Token() string { return c.token }

// TokenSource names where the credential came from: "env", "file", or "".
func (c RefImageUploadConfig) TokenSource() string { return c.tokenSource }

// TokenConfigured reports whether a credential was injected.
func (c RefImageUploadConfig) TokenConfigured() bool { return strings.TrimSpace(c.token) != "" }

// UploadTimeout is the per-request upload deadline.
func (c RefImageUploadConfig) UploadTimeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

var refImageUploadConfigCache atomic.Pointer[RefImageUploadConfig]

// GetRefImageUploadConfig returns the process-wide configuration, loading it
// from the environment on first use.
func GetRefImageUploadConfig() RefImageUploadConfig {
	if cached := refImageUploadConfigCache.Load(); cached != nil {
		return *cached
	}
	loaded := LoadRefImageUploadConfig()
	refImageUploadConfigCache.Store(&loaded)
	return loaded
}

// ResetRefImageUploadConfigCache drops the cached configuration. Used by tests
// and by an operator-triggered configuration reload.
func ResetRefImageUploadConfigCache() { refImageUploadConfigCache.Store(nil) }

// LoadRefImageUploadConfig reads and validates startup-only configuration. An
// invalid configuration disables the bridge instead of failing startup; the
// reason is logged without any secret material.
func LoadRefImageUploadConfig() RefImageUploadConfig {
	config := RefImageUploadConfig{
		Enabled:        common.GetEnvOrDefaultBool(RefImageUploadEnabledEnv, false),
		Endpoint:       common.GetEnvOrDefaultString(RefImageUploadEndpointEnv, DefaultRefImageUploadEndpoint),
		TimeoutSeconds: common.GetEnvOrDefault(RefImageUploadTimeoutSecondsEnv, DefaultRefImageUploadTimeoutSeconds),
	}
	for _, environment := range []string{RefImageUploadTokenEnv, RefImageUploadTokenFileEnv} {
		if raw, present := os.LookupEnv(environment); present && strings.TrimSpace(raw) == "" {
			common.SysError("refimage upload configuration is invalid: " + environment + " is set but empty; the bridge stays disabled")
			config.Enabled = false
			return config
		}
	}
	token, source, tokenErr := resolveRefImageUploadToken()
	if tokenErr != nil {
		common.SysError("refimage upload configuration is invalid: " + tokenErr.Error() + "; the bridge stays disabled")
		config.Enabled = false
		return config
	}
	config.token = token
	config.tokenSource = source
	if err := ValidateRefImageUploadConfig(config); err != nil {
		common.SysError("refimage upload configuration is invalid: " + err.Error() + "; the bridge stays disabled")
		config.Enabled = false
		return config
	}
	return config
}

// RefImageUploadCredentialRef documents the only accepted credential injection
// references.
func RefImageUploadCredentialRef() string {
	return "env:" + RefImageUploadTokenEnv + " | file:" + RefImageUploadTokenFileEnv
}

// resolveRefImageUploadToken reads the credential from REFIMAGE_UPLOAD_TOKEN,
// or from the file named by REFIMAGE_UPLOAD_TOKEN_FILE when no direct value is
// present.
func resolveRefImageUploadToken() (string, string, error) {
	rawPath := common.GetEnvOrDefaultString(RefImageUploadTokenFileEnv, "")
	path := strings.TrimSpace(rawPath)
	if path != "" {
		if path != rawPath || len(path) > MaxRefImageUploadTokenPathBytes {
			return "", "", errors.New("token file path syntax is invalid")
		}
		for _, character := range path {
			if unicode.IsControl(character) {
				return "", "", errors.New("token file path syntax is invalid")
			}
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", "", errors.New("token file cannot be read")
		}
		token := strings.TrimSpace(string(content))
		if err := validateRefImageUploadToken(token); err != nil {
			return "", "", err
		}
		return token, "file", nil
	}
	token := common.GetEnvOrDefaultString(RefImageUploadTokenEnv, "")
	if token == "" {
		return "", "", nil
	}
	if err := validateRefImageUploadToken(token); err != nil {
		return "", "", err
	}
	return token, "env", nil
}

func validateRefImageUploadToken(token string) error {
	if token == "" {
		return errors.New("token is empty")
	}
	if token != strings.TrimSpace(token) || len(token) > MaxRefImageUploadTokenBytes {
		return errors.New("token syntax is invalid")
	}
	for _, character := range token {
		if unicode.IsControl(character) {
			return errors.New("token syntax is invalid")
		}
	}
	return nil
}

// ValidateRefImageUploadConfig performs syntax checks only. It never resolves
// hosts, contacts an endpoint, or verifies credentials.
func ValidateRefImageUploadConfig(config RefImageUploadConfig) error {
	if config.TimeoutSeconds <= 0 || config.TimeoutSeconds > MaxRefImageUploadTimeoutSeconds {
		return fmt.Errorf("upload timeout must be between 1 and %d seconds", MaxRefImageUploadTimeoutSeconds)
	}
	if config.Endpoint != strings.TrimSpace(config.Endpoint) || config.Endpoint == "" {
		return errors.New("endpoint must be an absolute URL without surrounding whitespace")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Opaque != "" {
		return errors.New("endpoint must be an absolute URL without userinfo")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return errors.New("endpoint must use http or https")
	}
	if endpoint.Fragment != "" {
		return errors.New("endpoint must not contain a fragment")
	}
	return nil
}
