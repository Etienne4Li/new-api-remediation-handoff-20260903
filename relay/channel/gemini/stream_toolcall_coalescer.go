package gemini

import (
	"sort"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// geminiToolCallCoalescer repairs upstreams that stream a single Gemini
// functionCall as several parts, typically
//
//	{"functionCall":{"name":"web_fetch","args":{}}}
//	{"functionCall":{"name":"","args":{"url":"..."}}}
//
// followed by an empty chunk carrying finishReason. Genuine Gemini emits a
// functionCall part atomically, so a functionCall part with an empty name can
// only be a continuation of the previous call on the same candidate. Without
// this repair every fragment becomes its own OpenAI tool_call (the second one
// without a name) and strict clients reject the whole turn.
//
// The coalescer withholds a named functionCall until the next non-fragment
// part, the candidate's finishReason, or the end of the stream, merging the
// args of any nameless fragments into it, then releases it as one part.
type geminiToolCallCoalescer struct {
	pending map[int64]*dto.GeminiPart
	// Repaired reports whether any nameless fragment was merged; used for logging.
	Repaired bool
}

func newGeminiToolCallCoalescer() *geminiToolCallCoalescer {
	return &geminiToolCallCoalescer{pending: make(map[int64]*dto.GeminiPart)}
}

// process rewrites resp in place. emit is false when the chunk carried nothing
// but withheld function-call fragments and should not be forwarded; changed is
// true when the chunk content differs from what upstream sent.
func (s *geminiToolCallCoalescer) process(resp *dto.GeminiChatResponse) (emit bool, changed bool) {
	if s == nil || resp == nil || len(resp.Candidates) == 0 {
		return true, false
	}
	hasPayload := false
	for ci := range resp.Candidates {
		cand := &resp.Candidates[ci]
		idx := cand.Index
		parts := cand.Content.Parts
		candChanged := false
		out := make([]dto.GeminiPart, 0, len(parts)+1)
		for pi := range parts {
			part := parts[pi]
			if part.FunctionCall == nil {
				if p := s.pending[idx]; p != nil {
					out = append(out, *p)
					delete(s.pending, idx)
					candChanged = true
				}
				out = append(out, part)
				continue
			}
			if part.FunctionCall.FunctionName == "" {
				p := s.pending[idx]
				if p == nil {
					// Orphan fragment with nothing to attach to: forwarding it
					// would only produce a nameless tool call, so drop it.
					candChanged = true
					continue
				}
				p.FunctionCall.Arguments = mergeGeminiFunctionArgs(p.FunctionCall.Arguments, part.FunctionCall.Arguments)
				if len(p.ThoughtSignature) == 0 && len(part.ThoughtSignature) > 0 {
					p.ThoughtSignature = part.ThoughtSignature
				}
				s.Repaired = true
				candChanged = true
				continue
			}
			// Named call: release any previous one, withhold this one.
			if p := s.pending[idx]; p != nil {
				out = append(out, *p)
			}
			cp := part
			cp.FunctionCall = &dto.FunctionCall{
				FunctionName: part.FunctionCall.FunctionName,
				Arguments:    part.FunctionCall.Arguments,
			}
			s.pending[idx] = &cp
			candChanged = true
		}
		finished := cand.FinishReason != nil && *cand.FinishReason != ""
		if finished {
			if p := s.pending[idx]; p != nil {
				out = append(out, *p)
				delete(s.pending, idx)
				candChanged = true
			}
		}
		if candChanged {
			cand.Content.Parts = out
			changed = true
		}
		if len(cand.Content.Parts) > 0 || finished {
			hasPayload = true
		}
	}
	if changed && !hasPayload && resp.PromptFeedback == nil {
		return false, true
	}
	return true, changed
}

// flush returns a synthetic final chunk carrying any still-withheld calls, or
// nil. Call it once after the upstream stream ended.
func (s *geminiToolCallCoalescer) flush() *dto.GeminiChatResponse {
	if s == nil || len(s.pending) == 0 {
		return nil
	}
	keys := make([]int64, 0, len(s.pending))
	for k := range s.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	resp := &dto.GeminiChatResponse{}
	for _, k := range keys {
		stop := "STOP"
		resp.Candidates = append(resp.Candidates, dto.GeminiChatCandidate{
			Index:        k,
			FinishReason: &stop,
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{*s.pending[k]},
			},
		})
	}
	s.pending = make(map[int64]*dto.GeminiPart)
	return resp
}

// mergeGeminiFunctionArgs folds a fragment's args into the pending call's args.
// Object fragments are shallow-merged (later keys win); an empty or nil
// fragment leaves the pending args untouched; any other shape replaces them.
func mergeGeminiFunctionArgs(pending any, fragment any) any {
	fragMap, fragIsMap := fragment.(map[string]any)
	if fragment == nil || (fragIsMap && len(fragMap) == 0) {
		if pending == nil {
			return map[string]any{}
		}
		return pending
	}
	pendMap, pendIsMap := pending.(map[string]any)
	if fragIsMap && (pending == nil || pendIsMap) {
		merged := make(map[string]any, len(pendMap)+len(fragMap))
		for k, v := range pendMap {
			merged[k] = v
		}
		for k, v := range fragMap {
			merged[k] = v
		}
		return merged
	}
	return fragment
}
