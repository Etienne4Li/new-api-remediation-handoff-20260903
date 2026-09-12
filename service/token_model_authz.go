package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// Token model authorization.
//
// A token may carry an explicit model allowlist (the "token model limit"). The
// distributor refuses a request whose model is not on that list before a
// channel is selected. The same question has to be answered earlier on the
// task-plugin path, where a multipart reference-image request can already have
// contacted the external image host by the time distribution runs. This file
// holds the single read-only implementation both call sites use, so the two
// cannot drift apart.

// TokenModelAccess is the outcome of the token model-limit authorization.
type TokenModelAccess int

const (
	// TokenModelAccessUnrestricted means the token carries no model allowlist,
	// so every model is authorized.
	TokenModelAccessUnrestricted TokenModelAccess = iota
	// TokenModelAccessAllowed means the token carries an allowlist and that
	// allowlist authorizes the requested model.
	TokenModelAccessAllowed
	// TokenModelAccessLimitEmpty means the token carries an allowlist but it is
	// empty or unreadable, so it authorizes no model at all.
	TokenModelAccessLimitEmpty
	// TokenModelAccessModelForbidden means the token carries an allowlist and
	// the requested model is not on it.
	TokenModelAccessModelForbidden
)

// AuthorizeTokenModelAccess reports whether the token bound to this request may
// use modelName.
//
// It is read-only: it inspects the context keys that TokenAuth published, never
// mutates them, never touches a database or the network, and never logs.
//
// The caller must pass a non-nil request context, which every middleware
// caller already guarantees.
func AuthorizeTokenModelAccess(c *gin.Context, modelName string) TokenModelAccess {
	if !common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
		return TokenModelAccessUnrestricted
	}
	value, present := common.GetContextKey(c, constant.ContextKeyTokenModelLimit)
	if !present {
		// The allowlist is enabled but empty: no model is allowed.
		return TokenModelAccessLimitEmpty
	}
	limit, _ := value.(map[string]bool)
	if limit == nil {
		limit = map[string]bool{}
	}
	if !TokenModelLimitAllows(limit, modelName) {
		return TokenModelAccessModelForbidden
	}
	return TokenModelAccessAllowed
}

// TokenModelLimitAllows reports whether a token model-limit map authorizes
// model. Exact name, wildcard-normalized name, and routing-normalized name
// (modifiers and legacy aliases stripped) are all accepted.
func TokenModelLimitAllows(limit map[string]bool, model string) bool {
	if limit[model] {
		return true
	}
	if formatted := ratio_setting.FormatMatchingModelName(model); limit[formatted] {
		return true
	}
	return limit[ratio_setting.RoutingMatchModelName(model)]
}
