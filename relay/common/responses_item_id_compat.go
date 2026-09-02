package common

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const responsesGenericItemIDPrefix = "item_"
const ginKeyResponsesInputItemIDNormalization = "responses_input_item_id_normalization"

// ResponsesInputItemIDNormalizationReport contains aggregate metadata only;
// complete provider-generated IDs and request content are never recorded.
type ResponsesInputItemIDNormalizationReport struct {
	Reasoning          int `json:"reasoning"`
	Message            int `json:"message"`
	FunctionCall       int `json:"function_call"`
	CustomToolCall     int `json:"custom_tool_call"`
	FunctionCallOutput int `json:"function_call_output"`
	CustomToolOutput   int `json:"custom_tool_call_output"`
}

func (r ResponsesInputItemIDNormalizationReport) Count() int {
	return r.Reasoning + r.Message + r.FunctionCall + r.CustomToolCall +
		r.FunctionCallOutput + r.CustomToolOutput
}

func RecordResponsesInputItemIDNormalization(c *gin.Context, report ResponsesInputItemIDNormalizationReport) {
	if c == nil || report.Count() == 0 {
		return
	}
	c.Set(ginKeyResponsesInputItemIDNormalization, report)
}

func AppendResponsesInputItemIDNormalizationAdminInfo(c *gin.Context, adminInfo map[string]interface{}) {
	if c == nil || adminInfo == nil {
		return
	}
	value, ok := c.Get(ginKeyResponsesInputItemIDNormalization)
	if !ok {
		return
	}
	report, ok := value.(ResponsesInputItemIDNormalizationReport)
	if !ok || report.Count() == 0 {
		return
	}
	adminInfo["responses_input_item_id_normalization"] = report
}

// isSyntheticResponsesMessageID recognizes the provider-specific message ID
// form observed in Responses-compatible APIs. Keep this check narrow so an
// arbitrary opaque ID is never rewritten by accident.
func isSyntheticResponsesMessageID(id string) bool {
	const responsePrefix = "resp_"
	const messageSuffix = "_msg"
	if !strings.HasPrefix(id, responsePrefix) || !strings.HasSuffix(id, messageSuffix) {
		return false
	}
	opaque := id[len(responsePrefix) : len(id)-len(messageSuffix)]
	if opaque == "" {
		return false
	}
	for _, char := range opaque {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

// NormalizeResponsesInputItemIDs repairs narrowly identified compatibility
// defects emitted by some Responses-compatible providers. Only known type and
// prefix mismatches are changed; all other bytes are left untouched.
func NormalizeResponsesInputItemIDs(body []byte) ([]byte, ResponsesInputItemIDNormalizationReport, error) {
	var report ResponsesInputItemIDNormalizationReport
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, report, nil
	}

	type replacement struct {
		path string
		id   string
		kind string
	}
	replacements := make([]replacement, 0)
	for index, item := range input.Array() {
		id := strings.TrimSpace(item.Get("id").String())
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "" && strings.TrimSpace(item.Get("role").String()) != "" {
			itemType = "message"
		}

		var normalizedID string
		switch {
		case itemType == "message" && isSyntheticResponsesMessageID(id):
			normalizedID = "msg_" + strings.TrimPrefix(id, "resp_")
		case itemType == "reasoning" && strings.HasPrefix(id, responsesGenericItemIDPrefix) && len(id) > len(responsesGenericItemIDPrefix):
			normalizedID = "rs_" + strings.TrimPrefix(id, responsesGenericItemIDPrefix)
		case itemType == "message" && strings.HasPrefix(id, responsesGenericItemIDPrefix) && len(id) > len(responsesGenericItemIDPrefix):
			normalizedID = "msg_" + strings.TrimPrefix(id, responsesGenericItemIDPrefix)
		case itemType == "custom_tool_call" && strings.HasPrefix(id, "fc_"):
			normalizedID = "ctc_" + id
		case itemType == "custom_tool_call_output" && strings.HasPrefix(id, "fco_"):
			normalizedID = "ctco_" + id
		case itemType == "function_call" && strings.HasPrefix(id, "ctc_"):
			normalizedID = "fc_" + id
		case itemType == "function_call_output" && strings.HasPrefix(id, "ctco_"):
			normalizedID = "fco_" + id
		default:
			continue
		}
		replacements = append(replacements, replacement{
			path: fmt.Sprintf("input.%d.id", index),
			id:   normalizedID,
			kind: itemType,
		})
	}

	if len(replacements) == 0 {
		return body, report, nil
	}
	result := body
	for _, patch := range replacements {
		next, err := sjson.SetBytes(result, patch.path, patch.id)
		if err != nil {
			return body, ResponsesInputItemIDNormalizationReport{}, err
		}
		result = next
		switch patch.kind {
		case "reasoning":
			report.Reasoning++
		case "message":
			report.Message++
		case "function_call":
			report.FunctionCall++
		case "custom_tool_call":
			report.CustomToolCall++
		case "function_call_output":
			report.FunctionCallOutput++
		case "custom_tool_call_output":
			report.CustomToolOutput++
		}
	}
	return result, report, nil
}
