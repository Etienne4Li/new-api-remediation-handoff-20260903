package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	"github.com/QuantumNous/new-api/service"
)

func getGeminiVideoURL(channel *model.Channel, task *model.Task, apiKey string) (string, error) {
	return getGeminiVideoURLWithContext(context.Background(), channel, task, apiKey)
}

func getGeminiVideoURLWithContext(ctx context.Context, channel *model.Channel, task *model.Task, apiKey string) (string, error) {
	if channel == nil || task == nil {
		return "", fmt.Errorf("invalid channel or task")
	}
	baseURL := geminiVideoAPIBaseURL(channel)

	// Task.Data is an outward-facing, redacted projection and FailReason is a
	// legacy storage field. Neither may outrank the complete signed URL kept in
	// PrivateData, or a valid storage signature can be replaced by its public
	// redacted form.
	if resultURL := strings.TrimSpace(task.PrivateData.ResultURL); resultURL != "" &&
		!isTaskProxyContentURL(resultURL, task.TaskID) {
		return sanitizeGeminiVideoURL(baseURL, resultURL), nil
	}

	adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channel.Type)))
	legacyURL := ""
	if len(task.Data) != 0 {
		parsedTaskID := ""
		if adaptor != nil {
			persistedInfo, _ := adaptor.ParseTaskResult(task.Data)
			if persistedInfo != nil {
				parsedTaskID = persistedInfo.TaskID
			}
		}
		if err := validateVideoProxyResponseIdentity(task, parsedTaskID, task.Data); err != nil {
			return "", err
		}
		legacyURL = strings.TrimSpace(extractGeminiVideoURLFromTaskData(task))
	}

	var liveErr error
	switch {
	case adaptor == nil:
		liveErr = fmt.Errorf("gemini task adaptor not found")
	case strings.TrimSpace(apiKey) == "":
		liveErr = fmt.Errorf("api key not available for task")
	default:
		proxy := channel.GetSetting().Proxy
		if ctx == nil {
			ctx = context.Background()
		}
		fetchCtx, fetchCancel := context.WithTimeout(ctx, service.TaskPollingRequestTimeout)
		resp, err := service.FetchTaskWithContext(fetchCtx, adaptor, baseURL, apiKey, map[string]any{
			"task_id": task.GetUpstreamTaskID(),
			"action":  task.Action,
		}, proxy)
		if err != nil {
			fetchCancel()
			liveErr = fmt.Errorf("fetch task failed: %w", err)
		} else if resp == nil || resp.Body == nil {
			fetchCancel()
			liveErr = fmt.Errorf("fetch task returned empty response")
		} else {
			defer fetchCancel()
			defer resp.Body.Close()
			body, readErr := readVideoProxyTaskResponse(resp)
			if readErr != nil {
				liveErr = readErr
			} else {
				taskInfo, parseErr := adaptor.ParseTaskResult(body)
				parsedTaskID := ""
				if taskInfo != nil {
					parsedTaskID = taskInfo.TaskID
				}
				// Check both independent identity sources before looking at status
				// or URLs. An explicit mismatch must never fall back to a legacy URL.
				if err := validateVideoProxyResponseIdentity(task, parsedTaskID, body); err != nil {
					return "", err
				}
				switch {
				case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
					liveErr = fmt.Errorf("fetch task returned status %d", resp.StatusCode)
				case parseErr == nil && taskInfo != nil && strings.TrimSpace(taskInfo.RemoteUrl) != "":
					return sanitizeGeminiVideoURL(baseURL, strings.TrimSpace(taskInfo.RemoteUrl)), nil
				case parseErr == nil && taskInfo != nil && strings.TrimSpace(taskInfo.Url) != "":
					return sanitizeGeminiVideoURL(baseURL, strings.TrimSpace(taskInfo.Url)), nil
				case strings.TrimSpace(extractGeminiVideoURLFromPayload(body)) != "":
					return sanitizeGeminiVideoURL(baseURL, strings.TrimSpace(extractGeminiVideoURLFromPayload(body))), nil
				case parseErr != nil:
					liveErr = fmt.Errorf("parse task result failed: %w", parseErr)
				default:
					liveErr = fmt.Errorf("gemini video url not found")
				}
			}
		}
	}

	if legacyURL != "" {
		return sanitizeGeminiVideoURL(baseURL, legacyURL), nil
	}
	if historicalURL := strings.TrimSpace(task.FailReason); historicalURL != "" &&
		!isTaskProxyContentURL(historicalURL, task.TaskID) {
		return sanitizeGeminiVideoURL(baseURL, historicalURL), nil
	}
	if liveErr != nil {
		return "", liveErr
	}
	return "", fmt.Errorf("gemini video url not found")
}

// validateVideoProxyResponseIdentity checks the two independent identity
// sources exposed by Gemini/Vertex operation responses. Parsers normalize the
// provider operation name into TaskID, while the raw top-level name protects
// this boundary even when parsing otherwise fails. Missing identities remain
// compatible with historical provider payloads; explicit malformed or
// mismatched identities fail closed.
func validateVideoProxyResponseIdentity(task *model.Task, parsedTaskID string, body []byte) error {
	if err := service.ValidateTaskPollingResponseIdentity(task, parsedTaskID); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil || payload == nil {
		// The caller owns the authoritative parse error. With no readable raw
		// identity there is nothing additional for this identity fence to check.
		return nil
	}
	rawName, exists := payload["name"]
	if !exists || rawName == nil {
		return nil
	}
	operationName, ok := rawName.(string)
	if !ok {
		return service.ErrTaskPollingIdentityMismatch
	}
	rawTaskID := taskcommon.EncodeLocalTaskID(operationName)
	if rawTaskID == "" {
		return nil
	}
	return service.ValidateTaskPollingResponseIdentity(task, rawTaskID)
}

func extractGeminiVideoURLFromTaskData(task *model.Task) string {
	if task == nil || len(task.Data) == 0 {
		return ""
	}
	var payload map[string]any
	if err := common.Unmarshal(task.Data, &payload); err != nil {
		return ""
	}
	return extractGeminiVideoURLFromMap(payload)
}

func extractGeminiVideoURLFromPayload(body []byte) string {
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return extractGeminiVideoURLFromMap(payload)
}

func extractGeminiVideoURLFromMap(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if uri, ok := payload["uri"].(string); ok && uri != "" {
		return uri
	}
	if resp, ok := payload["response"].(map[string]any); ok {
		if uri := extractGeminiVideoURLFromResponse(resp); uri != "" {
			return uri
		}
	}
	return ""
}

func extractGeminiVideoURLFromResponse(resp map[string]any) string {
	if resp == nil {
		return ""
	}
	if gvr, ok := resp["generateVideoResponse"].(map[string]any); ok {
		if uri := extractGeminiVideoURLFromGeneratedSamples(gvr); uri != "" {
			return uri
		}
	}
	if videos, ok := resp["videos"].([]any); ok {
		for _, video := range videos {
			if vm, ok := video.(map[string]any); ok {
				if uri, ok := vm["uri"].(string); ok && uri != "" {
					return uri
				}
			}
		}
	}
	if uri, ok := resp["video"].(string); ok && uri != "" {
		return uri
	}
	if uri, ok := resp["uri"].(string); ok && uri != "" {
		return uri
	}
	return ""
}

func extractGeminiVideoURLFromGeneratedSamples(gvr map[string]any) string {
	if gvr == nil {
		return ""
	}
	if samples, ok := gvr["generatedSamples"].([]any); ok {
		for _, sample := range samples {
			if sm, ok := sample.(map[string]any); ok {
				if video, ok := sm["video"].(map[string]any); ok {
					if uri, ok := video["uri"].(string); ok && uri != "" {
						return uri
					}
				}
			}
		}
	}
	return ""
}

func getVertexVideoURL(channel *model.Channel, task *model.Task) (string, error) {
	return getVertexVideoURLWithContext(context.Background(), channel, task)
}

func getVertexVideoURLWithContext(ctx context.Context, channel *model.Channel, task *model.Task) (string, error) {
	if channel == nil || task == nil {
		return "", fmt.Errorf("invalid channel or task")
	}
	if url := strings.TrimSpace(task.PrivateData.ResultURL); url != "" && !isTaskProxyContentURL(url, task.TaskID) {
		return url, nil
	}

	baseURL := constant.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() != "" {
		baseURL = channel.GetBaseURL()
	}

	adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channel.Type)))
	legacyURL := ""
	if len(task.Data) != 0 {
		parsedTaskID := ""
		if adaptor != nil {
			persistedInfo, _ := adaptor.ParseTaskResult(task.Data)
			if persistedInfo != nil {
				parsedTaskID = persistedInfo.TaskID
			}
		}
		if err := validateVideoProxyResponseIdentity(task, parsedTaskID, task.Data); err != nil {
			return "", err
		}
		storedURL := strings.TrimSpace(extractVertexVideoURLFromTaskData(task))
		if isVideoProxyDataURL(storedURL) {
			return storedURL, nil
		}
		legacyURL = storedURL
	}

	key := getVertexTaskKey(channel, task)
	var liveErr error
	switch {
	case adaptor == nil:
		liveErr = fmt.Errorf("vertex task adaptor not found")
	case key == "":
		liveErr = fmt.Errorf("vertex key not available for task")
	default:
		if ctx == nil {
			ctx = context.Background()
		}
		fetchCtx, fetchCancel := context.WithTimeout(ctx, service.TaskPollingRequestTimeout)
		resp, err := service.FetchTaskWithContext(fetchCtx, adaptor, baseURL, key, map[string]any{
			"task_id": task.GetUpstreamTaskID(),
			"action":  task.Action,
		}, channel.GetSetting().Proxy)
		if err != nil {
			fetchCancel()
			liveErr = fmt.Errorf("fetch task failed: %w", err)
		} else if resp == nil || resp.Body == nil {
			fetchCancel()
			liveErr = fmt.Errorf("fetch task returned empty response")
		} else {
			defer fetchCancel()
			defer resp.Body.Close()
			body, readErr := readVideoProxyTaskResponse(resp)
			if readErr != nil {
				liveErr = readErr
			} else {
				taskInfo, parseErr := adaptor.ParseTaskResult(body)
				parsedTaskID := ""
				if taskInfo != nil {
					parsedTaskID = taskInfo.TaskID
				}
				if err := validateVideoProxyResponseIdentity(task, parsedTaskID, body); err != nil {
					return "", err
				}
				switch {
				case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
					liveErr = fmt.Errorf("fetch task returned status %d", resp.StatusCode)
				case parseErr == nil && taskInfo != nil && strings.TrimSpace(taskInfo.Url) != "":
					return strings.TrimSpace(taskInfo.Url), nil
				case parseErr == nil && taskInfo != nil && strings.TrimSpace(taskInfo.RemoteUrl) != "":
					return strings.TrimSpace(taskInfo.RemoteUrl), nil
				case strings.TrimSpace(extractVertexVideoURLFromPayload(body)) != "":
					return strings.TrimSpace(extractVertexVideoURLFromPayload(body)), nil
				case parseErr != nil:
					liveErr = fmt.Errorf("parse task result failed: %w", parseErr)
				default:
					liveErr = fmt.Errorf("vertex video url not found")
				}
			}
		}
	}

	if legacyURL != "" {
		return legacyURL, nil
	}
	if historicalURL := strings.TrimSpace(task.FailReason); historicalURL != "" &&
		!isTaskProxyContentURL(historicalURL, task.TaskID) {
		return historicalURL, nil
	}
	if liveErr != nil {
		return "", liveErr
	}
	return "", fmt.Errorf("vertex video url not found")
}

func isTaskProxyContentURL(url string, taskID string) bool {
	if strings.TrimSpace(url) == "" || strings.TrimSpace(taskID) == "" {
		return false
	}
	return strings.Contains(url, "/v1/videos/"+taskID+"/content")
}

func getVertexTaskKey(channel *model.Channel, task *model.Task) string {
	if task != nil {
		if key := strings.TrimSpace(task.PrivateData.Key); key != "" {
			return key
		}
	}
	if channel == nil {
		return ""
	}
	keys := channel.GetKeys()
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			return key
		}
	}
	return strings.TrimSpace(channel.Key)
}

func extractVertexVideoURLFromTaskData(task *model.Task) string {
	if task == nil || len(task.Data) == 0 {
		return ""
	}
	return extractVertexVideoURLFromPayload(task.Data)
}

func extractVertexVideoURLFromPayload(body []byte) string {
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return ""
	}
	resp, ok := payload["response"].(map[string]any)
	if !ok || resp == nil {
		return ""
	}

	if videos, ok := resp["videos"].([]any); ok && len(videos) > 0 {
		if video, ok := videos[0].(map[string]any); ok && video != nil {
			if b64, _ := video["bytesBase64Encoded"].(string); strings.TrimSpace(b64) != "" {
				mime, _ := video["mimeType"].(string)
				enc, _ := video["encoding"].(string)
				return buildVideoDataURL(mime, enc, b64)
			}
		}
	}
	if b64, _ := resp["bytesBase64Encoded"].(string); strings.TrimSpace(b64) != "" {
		enc, _ := resp["encoding"].(string)
		return buildVideoDataURL("", enc, b64)
	}
	if video, _ := resp["video"].(string); strings.TrimSpace(video) != "" {
		if strings.HasPrefix(video, "data:") || strings.HasPrefix(video, "http://") || strings.HasPrefix(video, "https://") {
			return video
		}
		enc, _ := resp["encoding"].(string)
		return buildVideoDataURL("", enc, video)
	}
	return ""
}

func buildVideoDataURL(mimeType string, encoding string, base64Data string) string {
	mime := strings.TrimSpace(mimeType)
	if mime == "" {
		enc := strings.TrimSpace(encoding)
		if enc == "" {
			enc = "mp4"
		}
		if strings.Contains(enc, "/") {
			mime = enc
		} else {
			mime = "video/" + enc
		}
	}
	return "data:" + mime + ";base64," + base64Data
}

func geminiVideoAPIBaseURL(channel *model.Channel) string {
	if channel != nil {
		if baseURL := strings.TrimSpace(channel.GetBaseURL()); baseURL != "" {
			return baseURL
		}
	}
	return constant.ChannelBaseURLs[constant.ChannelTypeGemini]
}

func sanitizeGeminiVideoURL(channelBaseURL, uri string) string {
	// A key-like query parameter on a signed storage URL belongs to that URL's
	// signature contract. Only Gemini API URLs may have API credentials moved
	// from the query into x-goog-api-key by VideoProxy.
	if strings.TrimSpace(uri) == "" {
		return uri
	}
	if !shouldAttachGeminiAPIKey(channelBaseURL, uri) {
		return uri
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		// The caller performs the authoritative URL parse/SSRF validation.  Do
		// not mutate an invalid value here.
		return uri
	}
	query := parsed.Query()
	removedCredential := false
	for name := range query {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "key", "api_key", "apikey", "x-goog-api-key":
			query.Del(name)
			removedCredential = true
		}
	}
	if !removedCredential {
		return uri
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
