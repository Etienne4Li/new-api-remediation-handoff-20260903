package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// WorkerRequest Worker请求的数据结构
type WorkerRequest struct {
	URL     string            `json:"url"`
	Key     string            `json:"key"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

const (
	// Downloads are also used by non-streaming image/audio paths.  They must
	// retain a finite deadline even when RELAY_TIMEOUT is intentionally zero for
	// long-lived streaming relay requests.
	defaultDownloadRequestTimeout = 60 * time.Second
	maxDownloadRequestTimeout     = 5 * time.Minute
)

// boundedDownloadRequestTimeout derives a safe per-request deadline from the
// global relay setting.  A positive RELAY_TIMEOUT is respected up to a hard
// cap; zero/negative values use the finite download default.
func boundedDownloadRequestTimeout(relayTimeoutSeconds int) time.Duration {
	if relayTimeoutSeconds <= 0 {
		return defaultDownloadRequestTimeout
	}
	seconds := int64(relayTimeoutSeconds)
	maxSeconds := int64(maxDownloadRequestTimeout / time.Second)
	if seconds > maxSeconds {
		return maxDownloadRequestTimeout
	}
	return time.Duration(seconds) * time.Second
}

// cancelOnCloseBody keeps the request context alive while the caller consumes
// the response body, then releases its timer as soon as the body is closed.
// If a caller forgets to close, the context deadline still bounds the leak.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}

func doBoundedHTTPClientRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		return nil, fmt.Errorf("http client is nil")
	}
	if req == nil {
		return nil, fmt.Errorf("http request is nil")
	}
	parent := req.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, boundedDownloadRequestTimeout(common.RelayTimeout))
	resp, err := client.Do(req.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		cancel()
		return resp, nil
	}
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// validateDownloadURL enforces the URL contract regardless of whether SSRF
// protection is enabled.  The latter is a policy toggle, not permission to
// pass arbitrary schemes or malformed hosts to a worker/proxy.
func validateDownloadURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if err := common.ValidateHTTPURL(trimmed); err != nil {
		return "", err
	}
	if err := ValidateSSRFProtectedFetchURL(trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// DoWorkerRequest 通过Worker发送请求
func DoWorkerRequest(req *WorkerRequest) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("worker request is nil")
	}
	systemConfig := system_setting.GetRuntimeConfig()
	if systemConfig.WorkerURL == "" {
		return nil, fmt.Errorf("worker not enabled")
	}
	normalizedURL, err := validateDownloadURL(req.URL)
	if err != nil {
		return nil, fmt.Errorf("request reject: %v", err)
	}
	if !systemConfig.WorkerAllowHttpImageRequestEnabled {
		parsed, parseErr := url.Parse(normalizedURL)
		if parseErr != nil || !strings.EqualFold(parsed.Scheme, "https") {
			return nil, fmt.Errorf("only support https url")
		}
	}
	req.URL = normalizedURL

	workerUrl := strings.TrimSpace(systemConfig.WorkerURL)
	if err := common.ValidateHTTPURL(workerUrl); err != nil {
		return nil, fmt.Errorf("invalid worker URL: %v", err)
	}
	if !strings.HasSuffix(workerUrl, "/") {
		workerUrl += "/"
	}

	// Serialize worker request data
	workerPayload, err := common.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal worker payload: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, workerUrl, bytes.NewBuffer(workerPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	return doBoundedHTTPClientRequest(GetHttpClient(), request)
}

func DoDownloadRequest(originUrl string, reason ...string) (resp *http.Response, err error) {
	normalizedURL, validationErr := validateDownloadURL(originUrl)
	if validationErr != nil {
		return nil, fmt.Errorf("request reject: %v", validationErr)
	}
	originUrl = normalizedURL
	systemConfig := system_setting.GetRuntimeConfig()
	if systemConfig.WorkerURL != "" {
		common.SysLog(fmt.Sprintf("downloading file from worker: url_meta=%s, reason: %s", common.SensitiveLogMeta(originUrl), strings.Join(reason, ", ")))
		req := &WorkerRequest{
			URL: originUrl,
			Key: systemConfig.WorkerValidKey,
		}
		return DoWorkerRequest(req)
	} else {
		common.SysLog(fmt.Sprintf("downloading from origin: url_meta=%s, reason: %s", common.SensitiveLogMeta(originUrl), strings.Join(reason, ", ")))
		request, err := http.NewRequest(http.MethodGet, originUrl, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create download request: %v", err)
		}
		return doBoundedHTTPClientRequest(GetSSRFProtectedHTTPClient(), request)
	}
}
