package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"golang.org/x/image/webp"
)

// The reference-image upload bridge converts multipart reference images that a
// client attached to a task-plugin submit request into public HTTPS URLs
// hosted by an external image host, then rewrites the plugin request fields so
// the plugin only ever sees URLs.
//
// Design constraints (see docs/development/refimage-upload.md):
//   - Only the task plugin key lietio-video is served; every other plugin key,
//     and every non-multipart or file-less request, is left untouched.
//   - Validate before upload, never upload before validation. The pinned
//     plugin decoder runs first against a *copy* of the protocol request whose
//     file fields carry non-leaking placeholder URLs (RefImageUploadVerifier).
//     Only a request the decoder accepts is allowed to reach the image host.
//     The placeholder body is discarded; it is never installed on the context
//     and never reaches the upstream.
//   - File bytes are read through the reusable request body storage
//     (common.ParseMultipartFormReusable) so the raw body stays intact and the
//     later BuildRequestBody multipart rebuild keeps working.
//   - The bridge runs before price calculation and pre-consumption, so a failed
//     upload neither pre-charges the caller nor reaches the upstream.
//   - Uploads expire after 24h on the image host (x-zipline-deletes-at).
//     Before submission an uploaded object cannot be associated with a task id,
//     so this process never deletes host objects; retention is left to the
//     host-side expiry.
//
// Failures are reported with one neutral message. Neither image-host nor
// upstream response bodies are ever surfaced to the caller.
const (
	// RefImageUploadDoneContextKey marks a request whose files were already
	// bridged, so a repeated call in the same request is a no-op.
	RefImageUploadDoneContextKey = "refimage_upload_bridge_done"

	// RefImageUploadNeutralMessage is the only text a client may see when the
	// bridge fails.
	RefImageUploadNeutralMessage = "reference image upload failed; the request was not submitted"

	// RefImageUploadRejectedPrefix opens the client text for a request the
	// pinned plugin decoder refused before any upload. The remainder is the
	// decoder's own sanitized message (pluginruntime.HookError.Message), the
	// same text the plugin hands to callers on the plain decode path.
	RefImageUploadRejectedPrefix = "request rejected before upload: "

	// refImageUploadFormField is the multipart field name sent to the image
	// host.
	refImageUploadFormField = "file"

	refImageUploadMaxResponseBytes = 64 << 10

	// refImageUploadSniffBytes is the prefix handed to http.DetectContentType.
	refImageUploadSniffBytes = 512

	// refImageUploadPlaceholderSegment is the reserved path segment a
	// pre-validation placeholder lives under. The value is derived from the
	// configured public prefix so the plugin decoder accepts it, but the
	// segment can never name a real hosted object and the placeholder body is
	// discarded after the check.
	refImageUploadPlaceholderSegment = "__refimage_pending__"
)

// The following limits are variables so tests can shrink them without
// materialising multi-megabyte payloads.
var (
	// RefImageUploadMaxFiles follows the largest image allowance in the
	// lietio-video plugin field matrix (referenceImages: 30 for Seedance-2.5)
	// and stays inside the endpoint middleware cap of 32 files.
	RefImageUploadMaxFiles = 30

	// RefImageUploadMaxFileBytes matches the image host per-file limit.
	RefImageUploadMaxFileBytes int64 = 25 << 20

	// refImageUploadMaxTotalBytes bounds one request's upload payload.
	refImageUploadMaxTotalBytes int64 = 120 << 20

	// RefImageUploadMaxDimension bounds one decoded edge. A header can claim
	// an arbitrarily large canvas while the file stays tiny, so the declared
	// dimensions are checked before the bytes are forwarded.
	RefImageUploadMaxDimension = 16384

	// RefImageUploadMaxPixels bounds width*height of one decoded image and is
	// the second half of the decode-bomb guard.
	RefImageUploadMaxPixels int64 = 40_000_000
)

// refImageUploadFieldTargets maps an accepted multipart file field name onto
// the plugin field that receives the hosted URL. Only the image fields of the
// lietio-video field matrix are served; reference videos and audios keep their
// existing URL-only contract.
var refImageUploadFieldTargets = map[string]string{
	"first_image":       "first_image",
	"input_reference":   "first_image",
	"image":             "first_image",
	"last_image":        "last_image",
	"referenceImages":   "referenceImages",
	"referenceImages[]": "referenceImages",
	"images":            "referenceImages",
}

// refImageUploadSingleTargets lists the targets that accept exactly one value.
var refImageUploadSingleTargets = map[string]bool{
	"first_image": true,
	"last_image":  true,
}

// refImageUploadTargetLimit is the per-target image allowance. It mirrors the
// tightest entry of the plugin model matrix; the plugin decoder itself remains
// authoritative through RefImageUploadVerifier, which runs before any upload.
// It is computed per call so RefImageUploadMaxFiles stays overridable by tests.
func refImageUploadTargetLimit(target string) (int, bool) {
	switch target {
	case "first_image", "last_image":
		return 1, true
	case "referenceImages":
		return RefImageUploadMaxFiles, true
	default:
		return 0, false
	}
}

// refImageUploadAllowedMimeTypes is the reference-image allowlist. Membership
// is decided by sniffing the file bytes, never by the part's declared type.
var refImageUploadAllowedMimeTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// refImageUploadExtensions is the extension written for each allowed type.
var refImageUploadExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// refImageUploadResponse is the accepted image-host response shape. Only
// files[0].url is read; every other field is ignored.
type refImageUploadResponse struct {
	Files []struct {
		URL string `json:"url"`
	} `json:"files"`
}

type refImageUploadPlan struct {
	target string
	// field is the multipart file field name exactly as the client sent it.
	// The probe and the rewritten request both carry the resulting URL under
	// this name as well as under target, so a decoder that reads the URL from
	// the field it received the file in (the file.field contract) sees the
	// same request before and after the upload.
	field    string
	file     *multipart.FileHeader
	mimeType string
}

type refImageUploadResult struct {
	target string
	field  string
	url    string
}

// RefImageUploadVerifier is the network-free pre-validation that must succeed
// before a single byte reaches the image host. It receives a field map in which
// every planned file is represented by a placeholder URL and must return nil
// only when the pinned plugin decoder accepts that request.
//
// Implementations must not perform network I/O and must not persist anything
// derived from the placeholder body.
type RefImageUploadVerifier func(placeholderFields map[string][]string) error

// refImageUploadHTTPClient returns the client used for uploads. It is a
// variable so tests can substitute a client that never reaches the network.
var refImageUploadHTTPClient = func() *http.Client {
	return newRefImageUploadHTTPClient()
}

// newRefImageUploadHTTPClient builds the dedicated upload client.
//
// It deliberately does not reuse service.GetHttpClient(): that client inherits
// the process proxy configuration and follows redirects. Both are credential
// leaks here, because the upload request carries the image host token in the
// Authorization header.
func newRefImageUploadHTTPClient() *http.Client {
	transport := &http.Transport{
		// Never inherit http.ProxyFromEnvironment: an egress proxy would see
		// the Authorization header. The image host is a fixed deployment
		// target reachable from the deployment network.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		// Bound the wait for response headers so a host that accepts the
		// connection and never answers cannot park the request.
		ResponseHeaderTimeout: 20 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		// A redirect would replay the Authorization header against a host we
		// did not choose. Return the 3xx response instead of following it; the
		// caller rejects every non-200 status.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// RefImageUploadBridged reports whether this request's reference images were
// already exchanged for hosted URLs. Consumers that would otherwise re-derive
// raw file parts from the request body must honour it.
func RefImageUploadBridged(c *gin.Context) bool {
	if c == nil {
		return false
	}
	bridged, exists := c.Get(RefImageUploadDoneContextKey)
	if !exists {
		return false
	}
	done, ok := bridged.(bool)
	return ok && done
}

// BridgeReferenceImageUpload uploads any multipart reference images attached to
// a lietio-video task-plugin request and rewrites the pinned protocol request
// fields with the resulting public HTTPS URLs.
//
// It is a no-op for other plugin keys, for non-multipart bodies, for requests
// without files, and for requests already bridged. When the bridge is enabled
// but no credential was injected, or when no verifier was supplied, it fails
// closed with the neutral message: a request is only ever uploaded after the
// pinned plugin decoder has accepted a placeholder copy of it.
func BridgeReferenceImageUpload(c *gin.Context, pluginKey string, verify RefImageUploadVerifier) error {
	if c == nil || c.Request == nil || strings.TrimSpace(pluginKey) != system_setting.RefImageUploadPluginKey {
		return nil
	}
	if RefImageUploadBridged(c) {
		return nil
	}
	config := system_setting.GetRefImageUploadConfig()
	if !config.Enabled {
		return nil
	}
	if !strings.Contains(c.GetHeader("Content-Type"), "multipart/form-data") {
		return nil
	}
	plan, present, cleanup, planErr := planRefImageUploads(c)
	if cleanup != nil {
		defer cleanup()
	}
	if planErr != nil {
		logRefImageUploadRejection(c, pluginKey, planErr.Error())
		return errors.New(RefImageUploadNeutralMessage)
	}
	if !present {
		return nil
	}
	if !config.TokenConfigured() {
		logRefImageUploadRejection(c, pluginKey, "credential_not_configured")
		return errors.New(RefImageUploadNeutralMessage)
	}
	// File bytes exist but there is no prepared plugin request body to rewrite:
	// the request cannot be bridged safely, so fail closed instead of letting
	// raw reference images through to the plugin decoder.
	protocol, body, fields := protocolRequestMultipartBody(c)
	if body == nil {
		logRefImageUploadRejection(c, pluginKey, "protocol_body_unavailable")
		return errors.New(RefImageUploadNeutralMessage)
	}
	// A served field that already holds a hosted URL must never also receive an
	// uploaded file: the two would be concatenated, which either exceeds the
	// field's arity or silently changes what the caller asked for.
	if conflictErr := refImageUploadFieldConflict(fields, plan); conflictErr != nil {
		logRefImageUploadRejection(c, pluginKey, conflictErr.Error())
		return errors.New(RefImageUploadNeutralMessage)
	}
	if verify == nil {
		logRefImageUploadRejection(c, pluginKey, "verifier_unavailable")
		return errors.New(RefImageUploadNeutralMessage)
	}
	// Validation before upload: the pinned plugin decoder judges a copy of the
	// request in which every planned file is a placeholder URL. Required
	// parameters, the per-model image count matrix, and field conflicts are
	// therefore settled before the image host is contacted.
	if verifyErr := verify(refImageUploadPlaceholderFields(fields, plan)); verifyErr != nil {
		logRefImageUploadRejection(c, pluginKey, "placeholder_decode_rejected")
		logger.LogWarn(c, "refimage_bridge event=decoder_detail plugin=%q detail=%q", pluginKey, verifyErr.Error())
		return errors.New(refImageUploadDecodeRejectionMessage(verifyErr))
	}
	uploadContext, cancel := context.WithTimeout(c.Request.Context(), config.UploadTimeout())
	defer cancel()
	uploaded, uploadErr := uploadRefImagePlan(uploadContext, config, plan)
	if uploadErr != nil {
		logRefImageUploadRejection(c, pluginKey, uploadErr.Error())
		return errors.New(RefImageUploadNeutralMessage)
	}
	for _, item := range uploaded {
		fields[item.target] = append(fields[item.target], item.url)
		// The placeholder probe holds a URL under the original file field name
		// as well; the rewritten request must match it or the decoder would
		// resolve a different request than the one that was validated.
		if item.field != "" && item.field != item.target {
			fields[item.field] = append(fields[item.field], item.url)
		}
	}
	body["fields"] = fields
	// Files are now represented by hosted URLs; nothing downstream may still
	// observe the raw binary attachments.
	body["files"] = []map[string]any{}
	clearRefImageUploadFiles(c, protocol)
	c.Set(RefImageUploadDoneContextKey, true)
	logger.LogDebug(
		c,
		"refimage_bridge event=bridged plugin=%q uploaded=%d",
		pluginKey,
		len(uploaded),
	)
	return nil
}

// refImageUploadFieldConflict rejects requests in which a served field already
// holds a hosted URL and a file would be appended to it. Appending would either
// exceed the field's arity or silently change the caller's request, and a
// single-value field must never end up holding two entries — including the case
// where the same target is reached through two different aliases.
func refImageUploadFieldConflict(fields map[string][]string, plan []refImageUploadPlan) error {
	existing := map[string]int{}
	for name, values := range fields {
		target, allowed := refImageUploadFieldTargets[strings.TrimSpace(name)]
		if !allowed {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				existing[target]++
			}
		}
	}
	if len(existing) == 0 {
		return nil
	}
	planned := map[string]int{}
	for _, item := range plan {
		planned[item.target]++
	}
	for target, count := range existing {
		if limit, capped := refImageUploadTargetLimit(target); capped && count+planned[target] > limit {
			return errors.New("field_conflict")
		}
	}
	return nil
}

// refImageUploadPlaceholderFields copies the live field map and substitutes one
// placeholder URL per planned file. The live map is never mutated.
//
// The placeholder is written under both the plugin field (target) and the
// multipart field name the client actually used. A decoder that validates the
// `file.field` of raw parts never sees any part in the probe — the probe
// describes the *post-upload* request, in which every file has been replaced by
// a hosted URL — so such a decoder must find the reference in the field it
// received the file in. Without the source-name copy the probe would describe a
// request the decoder rejects, and the bridge would refuse to upload a request
// the real (post-upload) decoder accepts.
func refImageUploadPlaceholderFields(fields map[string][]string, plan []refImageUploadPlan) map[string][]string {
	placeholder := make(map[string][]string, len(fields))
	for name, values := range fields {
		placeholder[name] = append([]string(nil), values...)
	}
	for index, item := range plan {
		value := refImageUploadPlaceholderURL(index)
		placeholder[item.target] = append(placeholder[item.target], value)
		if item.field != "" && item.field != item.target {
			placeholder[item.field] = append(placeholder[item.field], value)
		}
	}
	return placeholder
}

// refImageUploadPlaceholderURL builds the pre-validation URL. It is derived
// from the configured public prefix so the plugin decoder applies its real
// field rules, and it lives under a reserved segment that can never name a
// hosted object. The value is only ever handed to the decoder copy.
func refImageUploadPlaceholderURL(index int) string {
	return system_setting.RefImageUploadPublicPrefix +
		refImageUploadPlaceholderSegment + "/pending-" + strconv.Itoa(index)
}

// clearRefImageUploadFiles drops the raw file list from both views of the
// request: the protocol body map and the route request struct field.
func clearRefImageUploadFiles(c *gin.Context, protocol pluginruntime.ProtocolRequestContext) {
	protocol.Files = nil
	c.Set(pluginruntime.ContextKeyProtocolRequest, protocol)
	if value, exists := c.Get(pluginruntime.ContextKeyRouteRequest); exists {
		if route, ok := value.(pluginruntime.RouteRequestContext); ok {
			route.Files = nil
			c.Set(pluginruntime.ContextKeyRouteRequest, route)
		}
	}
}

// protocolRequestMultipartBody returns the protocol request context and the
// mutable multipart body and field map of the pinned protocol request.
func protocolRequestMultipartBody(c *gin.Context) (pluginruntime.ProtocolRequestContext, map[string]any, map[string][]string) {
	value, exists := c.Get(pluginruntime.ContextKeyProtocolRequest)
	if !exists {
		return pluginruntime.ProtocolRequestContext{}, nil, nil
	}
	protocol, ok := value.(pluginruntime.ProtocolRequestContext)
	if !ok {
		return pluginruntime.ProtocolRequestContext{}, nil, nil
	}
	body, ok := protocol.Body.(map[string]any)
	if !ok || body["kind"] != string(pluginruntime.BodyMultipart) {
		return protocol, nil, nil
	}
	fields, _ := body["fields"].(map[string][]string)
	if fields == nil {
		fields = map[string][]string{}
	}
	return protocol, body, fields
}

// NewTaskPluginPlaceholderVerifier returns the network-free pre-validation for
// the pinned endpoint held on the context. The pinned plugin decoder runs
// against a copy of the protocol request whose file fields carry placeholder
// URLs; its answer is used only to accept or reject the request.
//
// The probe uses the same admission-timeout pattern as the real decode call, so
// an exhausted plugin execution pool cannot park the request indefinitely.
//
// A nil return means pre-validation is unavailable, and the bridge then refuses
// to upload.
func NewTaskPluginPlaceholderVerifier(c *gin.Context, pinned pluginruntime.PinnedEndpoint) RefImageUploadVerifier {
	if pinned.Plugin == nil {
		return nil
	}
	return NewTaskPluginPlaceholderVerifierForPlugin(c, pinned.Plugin)
}

// NewTaskPluginPlaceholderVerifierForPlugin builds the same check with an
// explicit decoder, for entry points that already know which plugin parses the
// request. Protocol, operation, and model still come from the pinned endpoint
// on the context, because those describe the wire contract the decoder must
// honour rather than the decoder itself.
func NewTaskPluginPlaceholderVerifierForPlugin(c *gin.Context, plugin *pluginruntime.LoadedPlugin) RefImageUploadVerifier {
	if c == nil || plugin == nil || plugin.Engine == nil {
		return nil
	}
	value, exists := c.Get(pluginruntime.ContextKeyPinnedEndpoint)
	if !exists {
		return nil
	}
	pinned, ok := value.(pluginruntime.PinnedEndpoint)
	if !ok {
		return nil
	}
	protocol, _, _ := protocolRequestMultipartBody(c)
	if protocol.Body == nil {
		return nil
	}
	return func(placeholderFields map[string][]string) error {
		probe := pluginruntime.ProtocolRequestContext{
			RouteRequestContext: pluginruntime.RouteRequestContext{
				Path:   protocol.Path,
				Method: protocol.Method,
				Params: protocol.Params,
				Query:  protocol.Query,
				Body: map[string]any{
					"kind":   string(pluginruntime.BodyMultipart),
					"fields": placeholderFields,
					"files":  []map[string]any{},
				},
			},
			Protocol:      pinned.Protocol,
			Operation:     pinned.Operation.Name,
			Model:         pinned.Model,
			UpstreamModel: pinned.MappedModel,
			Stream:        protocol.Stream,
		}
		resolvedValue, callErr := plugin.Engine.CallPathWithAdmissionTimeout(
			context.Background(),
			pluginruntime.DefaultCallTimeout,
			"protocols",
			[]string{pinned.Protocol, "decodeRequest"},
			probe.JSValue(),
		)
		if callErr != nil {
			return fmt.Errorf("plugin decoder rejected the request: %w", callErr)
		}
		resolved, ok := resolvedValue.(map[string]any)
		if !ok {
			return errors.New("plugin decoder returned a non-object result")
		}
		if kind, _ := resolved["kind"].(string); kind != string(pluginruntime.RouteTypeSubmit) {
			return errors.New("plugin decoder returned an unsupported route kind")
		}
		resolvedModel, _ := resolved["model"].(string)
		if resolvedModel != pinned.Model {
			return errors.New("plugin decoder resolved a different model")
		}
		return nil
	}
}

// planRefImageUploads reads the request files through the reusable body storage
// so the raw body can still be parsed again by BuildRequestBody, validates them
// against the reference-image contract, and returns the upload plan.
//
// The returned cleanup removes the temporary files created by this parse and
// must run only after every planned file has been read.
func planRefImageUploads(c *gin.Context) ([]refImageUploadPlan, bool, func(), error) {
	form, err := common.ParseMultipartFormReusable(c)
	if err != nil {
		return nil, false, nil, err
	}
	cleanup := func() { form.RemoveAll() }
	names := make([]string, 0, len(form.File))
	total := 0
	for name, headers := range form.File {
		names = append(names, name)
		total += len(headers)
	}
	if total == 0 {
		return nil, false, cleanup, nil
	}
	if total > RefImageUploadMaxFiles {
		return nil, false, cleanup, errors.New("too_many_files")
	}
	sort.Strings(names)
	plan := make([]refImageUploadPlan, 0, total)
	counts := map[string]int{}
	var size int64
	for _, name := range names {
		for _, header := range form.File[name] {
			if header == nil {
				return nil, false, cleanup, errors.New("invalid_file")
			}
			target, allowed := refImageUploadFieldTargets[strings.TrimSpace(name)]
			if !allowed {
				return nil, false, cleanup, errors.New("unsupported_file_field")
			}
			// Bound the declared size before the bytes are touched.
			if header.Size <= 0 || header.Size > RefImageUploadMaxFileBytes {
				return nil, false, cleanup, errors.New("file_too_large")
			}
			mediaType, validated := sniffRefImageUploadPart(header)
			if !validated {
				return nil, false, cleanup, errors.New("unsupported_file_type")
			}
			size += header.Size
			if size > refImageUploadMaxTotalBytes {
				return nil, false, cleanup, errors.New("total_size_exceeded")
			}
			counts[target]++
			if refImageUploadSingleTargets[target] && counts[target] > 1 {
				return nil, false, cleanup, errors.New("duplicate_single_file_field")
			}
			if limit, capped := refImageUploadTargetLimit(target); capped && counts[target] > limit {
				return nil, false, cleanup, errors.New("image_count_exceeded")
			}
			plan = append(plan, refImageUploadPlan{
				target:   target,
				field:    strings.TrimSpace(name),
				file:     header,
				mimeType: mediaType,
			})
		}
	}
	return plan, true, cleanup, nil
}

// sniffRefImageUploadPart decides the part's media type from its bytes and
// checks the decoded dimensions. The declared Content-Type is never trusted: it
// is attacker-controlled and would otherwise let arbitrary payloads through
// under an image label.
//
// The declared type may only ever narrow the result. A declared type that is
// itself an allowlisted image must agree with the sniffed bytes; a generic or
// absent declared type (application/octet-stream, empty) is ignored and the
// sniffed type is used.
func sniffRefImageUploadPart(header *multipart.FileHeader) (string, bool) {
	source, err := header.Open()
	if err != nil {
		return "", false
	}
	head := make([]byte, refImageUploadSniffBytes)
	read, readErr := io.ReadFull(source, head)
	source.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return "", false
	}
	head = head[:read]
	if len(head) == 0 {
		return "", false
	}
	mediaType := http.DetectContentType(head)
	if index := strings.Index(mediaType, ";"); index >= 0 {
		mediaType = mediaType[:index]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if !refImageUploadAllowedMimeTypes[mediaType] {
		return "", false
	}
	format, ok := refImageUploadDecodeFormat(header)
	if !ok {
		return "", false
	}
	// The decoded format must agree with the sniffed type; a file whose bytes
	// only look like one format at the front is rejected.
	if refImageUploadFormatMimeType(format) != mediaType {
		return "", false
	}
	if declared := declaredRefImageUploadType(header); declared != "" && declared != mediaType {
		return "", false
	}
	return mediaType, true
}

// declaredRefImageUploadType returns the part's declared media type when it is
// one of the allowlisted images, and "" for every other declaration. A generic
// or missing declaration carries no information and is not treated as a
// conflict.
func declaredRefImageUploadType(header *multipart.FileHeader) string {
	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if err != nil || !refImageUploadAllowedMimeTypes[mediaType] {
		return ""
	}
	return mediaType
}

// refImageUploadDecodeFormat reads the declared dimensions of an image and
// returns its format name. Only headers are parsed, so a malicious file cannot
// force a full allocation.
func refImageUploadDecodeFormat(header *multipart.FileHeader) (string, bool) {
	config, format, err := decodeRefImageUploadConfig(header, false)
	if err != nil {
		config, format, err = decodeRefImageUploadConfig(header, true)
	}
	if err != nil {
		return "", false
	}
	if config.Width <= 0 || config.Height <= 0 {
		return "", false
	}
	if config.Width > RefImageUploadMaxDimension || config.Height > RefImageUploadMaxDimension {
		return "", false
	}
	if int64(config.Width)*int64(config.Height) > RefImageUploadMaxPixels {
		return "", false
	}
	return format, true
}

func decodeRefImageUploadConfig(header *multipart.FileHeader, asWebP bool) (image.Config, string, error) {
	source, err := header.Open()
	if err != nil {
		return image.Config{}, "", err
	}
	defer source.Close()
	if asWebP {
		config, decodeErr := webp.DecodeConfig(source)
		if decodeErr != nil {
			return image.Config{}, "", decodeErr
		}
		return config, "webp", nil
	}
	return image.DecodeConfig(source)
}

// refImageUploadFormatMimeType maps a decoder format name onto its media type.
func refImageUploadFormatMimeType(format string) string {
	switch strings.ToLower(format) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func uploadRefImagePlan(ctx context.Context, config system_setting.RefImageUploadConfig, plan []refImageUploadPlan) ([]refImageUploadResult, error) {
	results := make([]refImageUploadResult, 0, len(plan))
	for _, item := range plan {
		hosted, err := uploadRefImage(ctx, config, item)
		if err != nil {
			return nil, err
		}
		results = append(results, refImageUploadResult{target: item.target, field: item.field, url: hosted})
	}
	return results, nil
}

func uploadRefImage(ctx context.Context, config system_setting.RefImageUploadConfig, item refImageUploadPlan) (string, error) {
	source, err := item.file.Open()
	if err != nil {
		return "", errors.New("open_failed")
	}
	defer source.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name":     refImageUploadFormField,
		"filename": refImageUploadFilename(item),
	})
	if disposition == "" {
		return "", errors.New("invalid_filename")
	}
	header.Set("Content-Disposition", disposition)
	// The sniffed type, not the client-declared one.
	header.Set("Content-Type", item.mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", errors.New("encode_failed")
	}
	written, err := io.Copy(part, io.LimitReader(source, RefImageUploadMaxFileBytes+1))
	if err != nil {
		return "", errors.New("read_failed")
	}
	if written > RefImageUploadMaxFileBytes {
		return "", errors.New("file_too_large")
	}
	if err = writer.Close(); err != nil {
		return "", errors.New("encode_failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", errors.New("request_failed")
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", config.Token())
	request.Header.Set("x-zipline-deletes-at", system_setting.RefImageUploadDeletesAt)
	// Copy the shared client so one request's deadline cannot leak into the
	// next, while the tuned transport (no proxy, no redirects) stays shared.
	client := *refImageUploadHTTPClient()
	client.Timeout = config.UploadTimeout()
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("transport_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, refImageUploadMaxResponseBytes))
		return "", errors.New("upload_rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, refImageUploadMaxResponseBytes+1))
	if err != nil {
		return "", errors.New("response_unreadable")
	}
	if len(raw) > refImageUploadMaxResponseBytes {
		return "", errors.New("response_unreadable")
	}
	var decoded refImageUploadResponse
	if err = common.Unmarshal(raw, &decoded); err != nil {
		return "", errors.New("response_invalid")
	}
	if len(decoded.Files) == 0 {
		return "", errors.New("response_invalid")
	}
	hosted := decoded.Files[0].URL
	if !ValidRefImageUploadURL(hosted, config.PublicPrefix()) {
		return "", errors.New("response_url_rejected")
	}
	return hosted, nil
}

// ValidRefImageUploadURL accepts only a single-line absolute HTTPS URL on the
// configured host, under the configured path prefix, with no query, fragment,
// userinfo, or embedded whitespace.
func ValidRefImageUploadURL(raw, prefix string) bool {
	value := strings.TrimSpace(raw)
	if value == "" || value != raw || strings.ContainsAny(value, " \t\r\n\\\"'<>") {
		return false
	}
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return false
	}
	expected, err := url.Parse(prefix)
	if err != nil || expected.Host == "" {
		return false
	}
	if !strings.EqualFold(parsed.Host, expected.Host) || parsed.Port() != expected.Port() {
		return false
	}
	return strings.HasPrefix(parsed.Path, expected.Path) && len(parsed.Path) > len(expected.Path)
}

// refImageUploadFilename builds a safe upload filename from the client
// filename plus the extension of the sniffed content type.
func refImageUploadFilename(item refImageUploadPlan) string {
	extension := refImageUploadExtensions[item.mimeType]
	base := strings.TrimSpace(item.file.Filename)
	if index := strings.LastIndexAny(base, `/\`); index >= 0 {
		base = base[index+1:]
	}
	if index := strings.LastIndex(base, "."); index > 0 {
		base = base[:index]
	}
	base = strings.Trim(base, ".")
	if base == "" {
		base = "reference"
	}
	if len(base) > 64 {
		base = base[:64]
	}
	return base + extension
}

// SetRefImageUploadHTTPClientForTesting overrides the upload HTTP client and
// returns a restore function. It exists only for tests, which must never reach
// the public network.
func SetRefImageUploadHTTPClientForTesting(client *http.Client) func() {
	previous := refImageUploadHTTPClient
	if client != nil {
		refImageUploadHTTPClient = func() *http.Client { return client }
	}
	return func() { refImageUploadHTTPClient = previous }
}

// RefImageUploadStatusLine is the operator-facing summary of the bridge
// configuration. It never contains the credential.
func RefImageUploadStatusLine() string {
	config := system_setting.GetRefImageUploadConfig()
	source := config.TokenSource()
	if source == "" {
		source = "none"
	}
	return "refimage_upload enabled=" + strconv.FormatBool(config.Enabled) +
		" endpoint=" + config.Endpoint +
		" credential_source=" + source
}

// refImageUploadURLPattern removes any URL a decoder message might echo, so
// the placeholder host and hosted object paths never reach a client.
var refImageUploadURLPattern = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)

// refImageUploadDecodeRejectionMessage turns a placeholder-probe failure into
// client text. Only a message authored by the plugin decoder (a JavaScript
// exception surfaced as HookError) is passed through; engine and verifier
// failures carry no plugin-authored text and keep the neutral message.
func refImageUploadDecodeRejectionMessage(err error) string {
	var hookErr *pluginruntime.HookError
	if !errors.As(err, &hookErr) || hookErr == nil {
		return RefImageUploadNeutralMessage
	}
	detail := strings.TrimSpace(hookErr.Message)
	if detail == "" {
		return RefImageUploadNeutralMessage
	}
	detail = refImageUploadURLPattern.ReplaceAllString(detail, "[url]")
	detail = strings.ReplaceAll(detail, refImageUploadPlaceholderSegment, "")
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return RefImageUploadNeutralMessage
	}
	return RefImageUploadRejectedPrefix + detail
}

func logRefImageUploadRejection(c *gin.Context, pluginKey, reason string) {
	logger.LogWarn(
		c,
		"refimage_bridge event=rejected plugin=%q reason=%s",
		pluginKey,
		reason,
	)
}
