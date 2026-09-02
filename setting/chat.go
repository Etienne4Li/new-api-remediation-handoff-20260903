package setting

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

var Chats = []map[string]string{
	//{
	//	"ChatGPT Next Web 官方示例": "https://app.nextchat.dev/#/?settings={\"key\":\"{key}\",\"url\":\"{address}\"}",
	//},
	{
		"Cherry Studio": "cherrystudio://providers/api-keys?v=1&data={cherryConfig}",
	},
	{
		"AionUI": "aionui://provider/add?v=1&data={aionuiConfig}",
	},
	{
		"流畅阅读": "fluentread",
	},
	{
		"CC Switch": "ccswitch",
	},
	{
		"DeepChat": "deepchat://provider/install?v=1&data={deepchatConfig}",
	},
	{
		"Lobe Chat 官方示例": "https://chat-preview.lobehub.com/?settings={\"keyVaults\":{\"openai\":{\"apiKey\":\"{key}\",\"baseURL\":\"{address}/v1\"}}}",
	},
	{
		"AI as Workspace": "https://aiaw.app/set-provider?provider={\"type\":\"openai\",\"settings\":{\"apiKey\":\"{key}\",\"baseURL\":\"{address}/v1\",\"compatibility\":\"strict\"}}",
	},
	{
		"AMA 问天": "ama://set-api-key?server={address}&key={key}",
	},
	{
		"OpenCat": "opencat://team/join?domain={address}&token={key}",
	},
}

var chatsMu sync.RWMutex

const (
	maxChatsEntries   = 64
	maxChatNameBytes  = 256
	maxChatURLBytes   = 16 * 1024
	maxChatsJSONBytes = 512 * 1024
	maxChatDecodePass = 5
	maxChatJSONDepth  = 8
)

var (
	chatPlaceholderPattern = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_]*)\}`)
	chatLiteralKeyPattern  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])sk-[a-z0-9][a-z0-9._~+/=-]{7,}`)
	chatRawTokenPattern    = regexp.MustCompile(`^[a-zA-Z0-9]{48}$`)
	chatBase64Pattern      = regexp.MustCompile(`^[A-Za-z0-9+/_=-]+$`)
)

var allowedChatPlaceholders = map[string]struct{}{
	"address":        {},
	"key":            {},
	"cherryConfig":   {},
	"aionuiConfig":   {},
	"deepchatConfig": {},
}

var keyBearingChatPlaceholders = map[string]struct{}{
	"key":            {},
	"cherryConfig":   {},
	"aionuiConfig":   {},
	"deepchatConfig": {},
}

var blockedChatSchemes = map[string]struct{}{
	"javascript":       {},
	"data":             {},
	"vbscript":         {},
	"file":             {},
	"filesystem":       {},
	"blob":             {},
	"about":            {},
	"chrome":           {},
	"chrome-extension": {},
	"view-source":      {},
}

type chatURLAnalysis struct {
	keyBearing bool
	unsafe     bool
}

func cloneChats(chats []map[string]string) []map[string]string {
	if chats == nil {
		return nil
	}
	clone := make([]map[string]string, len(chats))
	for i, chat := range chats {
		if chat == nil {
			continue
		}
		clone[i] = make(map[string]string, len(chat))
		for key, value := range chat {
			clone[i][key] = value
		}
	}
	return clone
}

func normalizedChatField(value string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return []rune(strings.ToLower(string(r)))[0]
		}
		return -1
	}, strings.TrimSpace(value))
}

func isSensitiveChatField(value string) bool {
	switch normalizedChatField(value) {
	case "key", "apikey", "xapikey", "accesstoken", "authorization", "auth", "token", "bearer", "secret", "secretkey", "credential", "password", "privatekey", "clientsecret", "accesskey", "sessiontoken":
		return true
	default:
		return false
	}
}

func decodeChatCandidates(source string) ([]string, error) {
	candidates := []string{source}
	current := source
	for i := 0; i < maxChatDecodePass; i++ {
		if !strings.Contains(current, "%") {
			break
		}
		decoded, err := url.PathUnescape(current)
		if err != nil {
			return nil, fmt.Errorf("malformed percent encoding")
		}
		if decoded == current {
			break
		}
		candidates = append(candidates, decoded)
		current = decoded
		if i == maxChatDecodePass-1 && strings.Contains(current, "%") {
			return nil, fmt.Errorf("URL encoding is too deeply nested")
		}
	}
	return candidates, nil
}

func isKeyBearingPlaceholder(value string) bool {
	_, ok := keyBearingChatPlaceholders[value]
	return ok
}

func isAllowedChatPlaceholder(value string) bool {
	_, ok := allowedChatPlaceholders[value]
	return ok
}

func placeholderValue(value string) (string, bool) {
	candidates, err := decodeChatCandidates(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, "{") && strings.HasSuffix(candidate, "}") {
			name := candidate[1 : len(candidate)-1]
			if isAllowedChatPlaceholder(name) {
				return name, true
			}
		}
	}
	return "", false
}

func inspectChatJSON(value any) (chatURLAnalysis, bool) {
	return inspectChatJSONDepth(value, 0)
}

func inspectChatJSONDepth(value any, depth int) (analysis chatURLAnalysis, parsedObject bool) {
	if depth > maxChatJSONDepth {
		return chatURLAnalysis{unsafe: true}, false
	}
	switch typed := value.(type) {
	case string:
		if chatLiteralKeyPattern.MatchString(typed) {
			return chatURLAnalysis{keyBearing: true, unsafe: true}, false
		}
		return analysis, false
	case map[string]any:
		parsedObject = true
		for key, child := range typed {
			if isSensitiveChatField(key) {
				analysis.keyBearing = true
				if stringValue, ok := child.(string); ok {
					placeholder, allowed := placeholderValue(stringValue)
					if !allowed || !isKeyBearingPlaceholder(placeholder) || chatRawTokenPattern.MatchString(strings.TrimSpace(stringValue)) || chatLiteralKeyPattern.MatchString(stringValue) {
						analysis.unsafe = true
					}
				} else {
					analysis.unsafe = true
				}
			}
			childAnalysis, _ := inspectChatJSONDepth(child, depth+1)
			analysis.keyBearing = analysis.keyBearing || childAnalysis.keyBearing
			analysis.unsafe = analysis.unsafe || childAnalysis.unsafe
		}
	case []any:
		for _, child := range typed {
			childAnalysis, _ := inspectChatJSONDepth(child, depth+1)
			analysis.keyBearing = analysis.keyBearing || childAnalysis.keyBearing
			analysis.unsafe = analysis.unsafe || childAnalysis.unsafe
		}
	}
	return analysis, parsedObject
}

func inspectEncodedChatJSON(value string) chatURLAnalysis {
	if len(value) < 8 || len(value) > maxChatURLBytes {
		return chatURLAnalysis{}
	}
	if !chatBase64Pattern.MatchString(value) {
		return chatURLAnalysis{}
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(value)
		if err != nil {
			continue
		}
		trimmed := bytes.TrimSpace(decoded)
		if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
			continue
		}
		var parsed any
		if err := common.Unmarshal(trimmed, &parsed); err != nil {
			continue
		}
		analysis, _ := inspectChatJSON(parsed)
		if analysis.keyBearing || analysis.unsafe {
			// An encoded provider object is opaque to the browser and cannot be
			// safely rewritten at launch time. Treat credential-bearing objects
			// as unsafe, including those containing placeholders.
			analysis.unsafe = true
			return analysis
		}
	}
	return chatURLAnalysis{}
}

func inspectChatQueryPart(part string) (chatURLAnalysis, error) {
	var analysis chatURLAnalysis
	if part == "" {
		return analysis, nil
	}
	for _, pair := range strings.Split(part, "&") {
		if pair == "" {
			continue
		}
		keyPart, valuePart := pair, ""
		if separator := strings.IndexByte(pair, '='); separator >= 0 {
			keyPart, valuePart = pair[:separator], pair[separator+1:]
		}
		keyCandidates, err := decodeChatCandidates(strings.ReplaceAll(keyPart, "+", " "))
		if err != nil {
			return analysis, err
		}
		valueCandidates, err := decodeChatCandidates(strings.ReplaceAll(valuePart, "+", " "))
		if err != nil {
			return analysis, err
		}
		for _, key := range keyCandidates {
			if !isSensitiveChatField(key) {
				continue
			}
			analysis.keyBearing = true
			allowedPlaceholder := false
			for _, value := range valueCandidates {
				if placeholder, ok := placeholderValue(value); ok && isKeyBearingPlaceholder(placeholder) {
					allowedPlaceholder = true
					break
				}
			}
			if !allowedPlaceholder {
				analysis.unsafe = true
			}
			break
		}
		for _, value := range valueCandidates {
			if chatLiteralKeyPattern.MatchString(value) {
				analysis.keyBearing = true
				analysis.unsafe = true
			}
			if chatRawTokenPattern.MatchString(strings.TrimSpace(value)) {
				for _, key := range keyCandidates {
					if isSensitiveChatField(key) {
						analysis.keyBearing = true
						analysis.unsafe = true
						break
					}
				}
			}
			if encodedAnalysis := inspectEncodedChatJSON(value); encodedAnalysis.keyBearing || encodedAnalysis.unsafe {
				analysis.keyBearing = true
				analysis.unsafe = analysis.unsafe || encodedAnalysis.unsafe
			}
			trimmed := strings.TrimSpace(value)
			if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
				continue
			}
			var parsed any
			if err := common.Unmarshal([]byte(trimmed), &parsed); err != nil {
				continue
			}
			jsonAnalysis, _ := inspectChatJSON(parsed)
			analysis.keyBearing = analysis.keyBearing || jsonAnalysis.keyBearing
			analysis.unsafe = analysis.unsafe || jsonAnalysis.unsafe
		}
	}
	return analysis, nil
}

func analyzeChatURL(value string) (chatURLAnalysis, error) {
	var analysis chatURLAnalysis
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxChatURLBytes || !utf8.ValidString(trimmed) {
		return analysis, fmt.Errorf("URL is empty, invalid, or too long")
	}
	if strings.IndexFunc(trimmed, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return analysis, fmt.Errorf("URL contains control characters")
	}
	if strings.HasPrefix(trimmed, "//") {
		return analysis, fmt.Errorf("protocol-relative URLs are not allowed")
	}
	if strings.EqualFold(trimmed, "fluentread") || strings.EqualFold(trimmed, "ccswitch") {
		return analysis, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return analysis, fmt.Errorf("invalid URL")
	}
	if parsed.Scheme == "" {
		return analysis, fmt.Errorf("URL must include a scheme")
	}
	if _, blocked := blockedChatSchemes[strings.ToLower(parsed.Scheme)]; blocked {
		return analysis, fmt.Errorf("URL scheme is not allowed")
	}
	if parsed.User != nil {
		return analysis, fmt.Errorf("URL userinfo is not allowed")
	}
	if (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Host == "" {
		return analysis, fmt.Errorf("HTTP URL must include a host")
	}
	candidates, err := decodeChatCandidates(trimmed)
	if err != nil {
		return analysis, err
	}
	for _, candidate := range candidates {
		if strings.IndexFunc(candidate, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return analysis, fmt.Errorf("URL contains control characters")
		}
		if candidate != trimmed {
			if decodedURL, decodeErr := url.Parse(candidate); decodeErr == nil && decodedURL.User != nil {
				return analysis, fmt.Errorf("URL userinfo is not allowed")
			}
		}
		for _, match := range chatPlaceholderPattern.FindAllStringSubmatch(candidate, -1) {
			name := match[1]
			if !isAllowedChatPlaceholder(name) {
				return analysis, fmt.Errorf("unsupported URL placeholder")
			}
			if isKeyBearingPlaceholder(name) {
				analysis.keyBearing = true
			}
		}
		if chatLiteralKeyPattern.MatchString(candidate) {
			analysis.keyBearing = true
			analysis.unsafe = true
		}
		queryStart := strings.IndexByte(candidate, '?')
		fragmentStart := strings.IndexByte(candidate, '#')
		if queryStart >= 0 {
			end := len(candidate)
			if fragmentStart > queryStart {
				end = fragmentStart
			}
			partAnalysis, partErr := inspectChatQueryPart(candidate[queryStart+1 : end])
			if partErr != nil {
				return analysis, partErr
			}
			analysis.keyBearing = analysis.keyBearing || partAnalysis.keyBearing
			analysis.unsafe = analysis.unsafe || partAnalysis.unsafe
		}
		if fragmentStart >= 0 {
			partAnalysis, partErr := inspectChatQueryPart(candidate[fragmentStart+1:])
			if partErr != nil {
				return analysis, partErr
			}
			analysis.keyBearing = analysis.keyBearing || partAnalysis.keyBearing
			analysis.unsafe = analysis.unsafe || partAnalysis.unsafe
		}
	}
	return analysis, nil
}

func validateChatEntry(name, value string) (chatURLAnalysis, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || len(trimmedName) > maxChatNameBytes || !utf8.ValidString(trimmedName) {
		return chatURLAnalysis{}, fmt.Errorf("chat name is empty, invalid, or too long")
	}
	if strings.IndexFunc(trimmedName, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return chatURLAnalysis{}, fmt.Errorf("chat name contains control characters")
	}
	return analyzeChatURL(value)
}

func decodeChatsJSON(jsonString string) ([]map[string]string, error) {
	trimmed := strings.TrimSpace(jsonString)
	if trimmed == "" || len(trimmed) > maxChatsJSONBytes || !strings.HasPrefix(trimmed, "[") {
		return nil, fmt.Errorf("Chats must be a JSON array")
	}
	var rawItems []json.RawMessage
	if err := common.Unmarshal([]byte(trimmed), &rawItems); err != nil {
		return nil, fmt.Errorf("invalid Chats JSON")
	}
	if len(rawItems) > maxChatsEntries {
		return nil, fmt.Errorf("too many chat presets")
	}
	decoded := make([]map[string]string, 0, len(rawItems))
	for index, rawItem := range rawItems {
		if first := bytes.TrimSpace(rawItem); len(first) == 0 || first[0] != '{' {
			return nil, fmt.Errorf("chat preset %d must be an object", index)
		}
		var item map[string]string
		if err := common.Unmarshal(rawItem, &item); err != nil || len(item) != 1 {
			return nil, fmt.Errorf("chat preset %d must contain one string key-value pair", index)
		}
		for name, value := range item {
			if _, err := validateChatEntry(name, value); err != nil {
				return nil, fmt.Errorf("chat preset %d is invalid", index)
			}
			decoded = append(decoded, map[string]string{strings.TrimSpace(name): strings.TrimSpace(value)})
		}
	}
	return decoded, nil
}

// ValidateChats validates the in-memory representation used by the settings
// publisher. It intentionally accepts key placeholders for backwards
// compatibility, while rejecting literal credentials and unsafe destinations.
func ValidateChats(chats []map[string]string) error {
	if len(chats) > maxChatsEntries {
		return fmt.Errorf("too many chat presets")
	}
	for index, item := range chats {
		if item == nil || len(item) != 1 {
			return fmt.Errorf("chat preset %d must contain one string key-value pair", index)
		}
		for name, value := range item {
			analysis, err := validateChatEntry(name, value)
			if err != nil || analysis.unsafe {
				return fmt.Errorf("chat preset %d is invalid", index)
			}
		}
	}
	return nil
}

// ValidateChatsJSON validates the persisted/admin-supplied JSON form of chat
// presets without exposing the supplied value in the returned error.
func ValidateChatsJSON(jsonString string) error {
	decoded, err := decodeChatsJSON(jsonString)
	if err != nil {
		return err
	}
	return ValidateChats(decoded)
}

// SanitizeChatsJSON drops malformed or credential-bearing legacy entries so a
// stale database value cannot prevent the process from loading other options.
// The returned value is always a valid JSON array and never contains secrets.
func SanitizeChatsJSON(jsonString string) string {
	trimmed := strings.TrimSpace(jsonString)
	if trimmed == "" || len(trimmed) > maxChatsJSONBytes || !strings.HasPrefix(trimmed, "[") {
		return "[]"
	}
	var rawItems []json.RawMessage
	if err := common.Unmarshal([]byte(trimmed), &rawItems); err != nil {
		return "[]"
	}
	clean := make([]map[string]string, 0, minInt(len(rawItems), maxChatsEntries))
	for index, rawItem := range rawItems {
		if index >= maxChatsEntries || len(bytes.TrimSpace(rawItem)) == 0 || bytes.TrimSpace(rawItem)[0] != '{' {
			continue
		}
		var item map[string]string
		if err := common.Unmarshal(rawItem, &item); err != nil || len(item) != 1 {
			continue
		}
		for name, value := range item {
			analysis, err := validateChatEntry(name, value)
			if err != nil || analysis.unsafe {
				continue
			}
			clean = append(clean, map[string]string{strings.TrimSpace(name): strings.TrimSpace(value)})
		}
	}
	bytesValue, err := common.Marshal(clean)
	if err != nil {
		return "[]"
	}
	return string(bytesValue)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GetChats returns a detached snapshot. Callers may safely mutate the result
// without racing a concurrent option hot-reload.
func GetChats() []map[string]string {
	chatsMu.RLock()
	defer chatsMu.RUnlock()
	return cloneChats(Chats)
}

// GetPublicChats returns only presets that can be sent to an unauthenticated
// browser without carrying a long-lived credential. The admin option API can
// still expose the full, placeholder-bearing configuration to an authorized
// administrator; this public snapshot deliberately cannot.
func GetPublicChats() []map[string]string {
	chatsMu.RLock()
	defer chatsMu.RUnlock()
	publicChats := make([]map[string]string, 0, len(Chats))
	for _, item := range Chats {
		if item == nil || len(item) != 1 {
			continue
		}
		for name, value := range item {
			analysis, err := validateChatEntry(name, value)
			if err != nil || analysis.keyBearing || analysis.unsafe {
				continue
			}
			publicChats = append(publicChats, map[string]string{
				strings.TrimSpace(name): strings.TrimSpace(value),
			})
		}
	}
	return publicChats
}

func UpdateChatsByJsonString(jsonString string) error {
	decoded, err := decodeChatsJSON(jsonString)
	if err != nil {
		return err
	}
	if err := ValidateChats(decoded); err != nil {
		return err
	}
	decoded = cloneChats(decoded)
	chatsMu.Lock()
	Chats = decoded
	chatsMu.Unlock()
	return nil
}

func Chats2JsonString() string {
	chatsMu.RLock()
	snapshot := cloneChats(Chats)
	chatsMu.RUnlock()
	if snapshot == nil {
		return "[]"
	}
	jsonBytes, err := common.Marshal(snapshot)
	if err != nil {
		common.SysLog("error marshalling chats: " + err.Error())
		return "[]"
	}
	return string(jsonBytes)
}
