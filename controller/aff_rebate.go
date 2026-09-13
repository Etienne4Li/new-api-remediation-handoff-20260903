package controller

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// affRebateItem is the wire shape of one rebate row. The invitee is reduced to
// a masked label on purpose: an inviter may learn that their friend topped up,
// never that friend's full email or username.
type affRebateItem struct {
	Id          int     `json:"id"`
	Invitee     string  `json:"invitee"`
	TopupMoney  float64 `json:"topup_money"`
	RebateQuota int     `json:"rebate_quota"`
	Sequence    int     `json:"sequence"`
	CreatedTime int64   `json:"created_time"`
}

type affRebateListResponse struct {
	Enabled  bool            `json:"enabled"`
	Percent  int             `json:"percent"`
	MaxTimes int             `json:"max_times"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int             `json:"total"`
	Items    []affRebateItem `json:"items"`
}

// maskIdentifierPart keeps the first keep runes of a label and replaces the
// rest with "***". Anything too short to partially hide is fully hidden.
func maskIdentifierPart(part string, keep int) string {
	runes := []rune(part)
	if len(runes) <= keep {
		return "***"
	}
	return string(runes[:keep]) + "***"
}

// maskInviteeIdentity renders an invitee label the inviter can recognise
// without exposing the address itself: "user@example.com" becomes
// "u***@ex***.com". Users without an email fall back to a masked username and
// finally to the opaque "用户 #<id>".
func maskInviteeIdentity(userId int, username string, email string) string {
	email = strings.TrimSpace(email)
	if at := strings.LastIndex(email, "@"); at > 0 && at < len(email)-1 {
		local := email[:at]
		domain := email[at+1:]
		maskedDomain := maskIdentifierPart(domain, 2)
		if dot := strings.Index(domain, "."); dot > 0 {
			maskedDomain = maskIdentifierPart(domain[:dot], 2) + domain[dot:]
		}
		return maskIdentifierPart(local, 1) + "@" + maskedDomain
	}

	if username = strings.TrimSpace(username); username != "" {
		if masked := maskIdentifierPart(username, 1); masked != "***" {
			return masked
		}
	}
	return fmt.Sprintf("用户 #%d", userId)
}

// GetAffRebates returns the current user's invitation rebate history together
// with the live rebate configuration, so the wallet page can render the rule
// text without a second request and without exposing the settings API.
func GetAffRebates(c *gin.Context) {
	userId := c.GetInt("id")
	pageInfo := common.GetPageQuery(c)

	response := affRebateListResponse{
		Enabled:  common.AffRebateEnabled,
		Percent:  common.AffRebatePercent,
		MaxTimes: common.AffRebateMaxTimes,
		Page:     pageInfo.GetPage(),
		PageSize: pageInfo.GetPageSize(),
		Items:    []affRebateItem{},
	}

	rebates, total, err := model.GetUserAffRebates(userId, pageInfo)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	response.Total = int(total)

	identities, err := model.GetAffRebateInviteeIdentities(rebates)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	for _, rebate := range rebates {
		identity := identities[rebate.InviteeId]
		response.Items = append(response.Items, affRebateItem{
			Id:          rebate.Id,
			Invitee:     maskInviteeIdentity(rebate.InviteeId, identity.Username, identity.Email),
			TopupMoney:  rebate.TopupMoney,
			RebateQuota: rebate.RebateQuota,
			Sequence:    rebate.Sequence,
			CreatedTime: rebate.CreatedTime,
		})
	}

	common.ApiSuccess(c, response)
}
