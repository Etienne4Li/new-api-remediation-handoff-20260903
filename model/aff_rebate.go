package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// AffRebate is one granted invitation rebate: the inviter received
// RebateQuota because invitee InviteeId completed top-up TopUpId.
//
// TopUpId carries a UNIQUE index and is the only idempotency guarantee.
// Payment callbacks retry, so the grant must be rejected by the database
// rather than by a read-then-write check that two concurrent callbacks can
// both pass.
type AffRebate struct {
	Id          int     `json:"id"`
	InviterId   int     `json:"inviter_id" gorm:"index"`
	InviteeId   int     `json:"invitee_id" gorm:"index"`
	TopUpId     int     `json:"top_up_id" gorm:"uniqueIndex;not null"`
	TopupMoney  float64 `json:"topup_money"`
	RebateQuota int     `json:"rebate_quota"`
	Sequence    int     `json:"sequence"`
	CreatedTime int64   `json:"created_time"`
}

// isDuplicateKeyError reports whether err is a unique-constraint violation.
//
// Correctness never depends on this classification: the UNIQUE index on
// top_up_id rejects the second insert either way, so a misclassified error
// only changes the log level, never the balance.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	// The drivers are only indirect dependencies here, so match on the message
	// instead of importing them for a typed assertion.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate entry") || // MySQL 1062
		strings.Contains(msg, "duplicate key value") || // PostgreSQL 23505
		strings.Contains(msg, "unique constraint failed") || // SQLite
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "1062") ||
		strings.Contains(msg, "23505")
}

// affRebateQuota converts a paid amount into the rebate quota using the same
// money -> quota path the top-up itself uses (money * QuotaPerUnit), with the
// percentage applied in decimal so no float rounding reaches the integer quota.
func affRebateQuota(money float64, percent int) (int, error) {
	return common.WalletQuotaFromDecimalStrict(
		decimal.NewFromFloat(money).
			Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
			Mul(decimal.NewFromInt(int64(percent))).
			Div(decimal.NewFromInt(100)),
	)
}

// GrantAffRebate credits the inviter of the paying user with AffRebatePercent%
// of the paid amount, for the invitee's first AffRebateMaxTimes successful
// top-ups only.
//
// It deliberately returns nothing: the rebate is a bonus on top of an already
// settled payment, so every failure is logged and swallowed. A rebate problem
// must never turn a successful top-up into a failed one.
//
// Call it after the top-up transaction has committed and the quota cache has
// been synced; it opens its own transaction.
func GrantAffRebate(topUp *TopUp) {
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf("aff rebate panicked, top-up stays successful: %v", r))
		}
	}()

	if !common.AffRebateEnabled {
		return
	}
	if topUp == nil || topUp.Id <= 0 || topUp.UserId <= 0 {
		return
	}
	// Only a settled top-up earns a rebate. Callers pass the in-memory order
	// they just marked successful; anything else is a programming error.
	if topUp.Status != common.TopUpStatusSuccess {
		return
	}
	if topUp.Money <= 0 {
		return
	}

	percent := common.AffRebatePercent
	maxTimes := common.AffRebateMaxTimes
	if percent <= 0 || maxTimes <= 0 {
		return
	}
	if percent > 100 {
		common.SysError(fmt.Sprintf("aff rebate percent %d is out of range, skipping rebate for top_up_id=%d", percent, topUp.Id))
		return
	}

	var invitee User
	if err := DB.Select("id", "inviter_id").Where("id = ?", topUp.UserId).First(&invitee).Error; err != nil {
		common.SysError(fmt.Sprintf("aff rebate failed to load invitee user_id=%d: %s", topUp.UserId, err.Error()))
		return
	}
	inviterId := invitee.InviterId
	if inviterId <= 0 {
		return
	}
	// Guards a corrupt inviter_id rather than abuse; see SPEC section 1.8.
	if inviterId == topUp.UserId {
		common.SysError(fmt.Sprintf("aff rebate skipped: user_id=%d is its own inviter", topUp.UserId))
		return
	}

	// Count by `id <=` instead of a full COUNT(*) so out-of-order callbacks and
	// admin back-fills still place this order at a stable position in the
	// invitee's top-up sequence.
	var sequence int64
	if err := DB.Model(&TopUp{}).
		Where("user_id = ? AND status = ? AND id <= ?", topUp.UserId, common.TopUpStatusSuccess, topUp.Id).
		Count(&sequence).Error; err != nil {
		common.SysError(fmt.Sprintf("aff rebate failed to count top-ups for user_id=%d: %s", topUp.UserId, err.Error()))
		return
	}
	if sequence <= 0 || sequence > int64(maxTimes) {
		return
	}

	rebateQuota, err := affRebateQuota(topUp.Money, percent)
	if err != nil || rebateQuota <= 0 {
		if err != nil {
			common.SysError(fmt.Sprintf("aff rebate quota conversion failed for top_up_id=%d money=%f: %s", topUp.Id, topUp.Money, err.Error()))
		}
		return
	}

	rebate := &AffRebate{
		InviterId:   inviterId,
		InviteeId:   topUp.UserId,
		TopUpId:     topUp.Id,
		TopupMoney:  topUp.Money,
		RebateQuota: rebateQuota,
		Sequence:    int(sequence),
		CreatedTime: common.GetTimestamp(),
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		// Insert first: a replayed callback fails here and aborts before any
		// balance is touched.
		if err := tx.Create(rebate).Error; err != nil {
			return err
		}
		// Same write shape as inviteUser(): aff_quota is the spendable balance,
		// aff_history the lifetime total. aff_count stays untouched, it counts
		// invitees and was already incremented at registration.
		result := tx.Model(&User{}).Where("id = ?", inviterId).Updates(map[string]interface{}{
			"aff_quota":   gorm.Expr("aff_quota + ?", rebateQuota),
			"aff_history": gorm.Expr("aff_history + ?", rebateQuota),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		if isDuplicateKeyError(err) {
			common.SysLog(fmt.Sprintf("aff rebate already granted for top_up_id=%d, skipping", topUp.Id))
			return
		}
		common.SysError(fmt.Sprintf("aff rebate failed for top_up_id=%d inviter_id=%d: %s", topUp.Id, inviterId, err.Error()))
		return
	}

	RecordLog(inviterId, LogTypeSystem, fmt.Sprintf("邀请充值返利 %s（被邀请人 #%d 第 %d 笔充值，支付金额 %.2f，返利比例 %d%%）",
		logger.LogQuota(rebateQuota), topUp.UserId, sequence, topUp.Money, percent))
}

// AffRebateInviteeIdentity carries the raw fields a caller needs to build a
// masked invitee label. It is never serialised to an API response as-is.
type AffRebateInviteeIdentity struct {
	Id       int
	Username string
	Email    string
}

// GetAffRebateInviteeIdentities loads the invitees referenced by the given
// rebates in one query, keyed by user id. Missing users are simply absent, so
// callers fall back to an id-only label.
func GetAffRebateInviteeIdentities(rebates []*AffRebate) (map[int]AffRebateInviteeIdentity, error) {
	identities := make(map[int]AffRebateInviteeIdentity, len(rebates))
	if len(rebates) == 0 {
		return identities, nil
	}

	ids := make([]int, 0, len(rebates))
	seen := make(map[int]bool, len(rebates))
	for _, rebate := range rebates {
		if rebate == nil || rebate.InviteeId <= 0 || seen[rebate.InviteeId] {
			continue
		}
		seen[rebate.InviteeId] = true
		ids = append(ids, rebate.InviteeId)
	}
	if len(ids) == 0 {
		return identities, nil
	}

	var rows []AffRebateInviteeIdentity
	if err := DB.Model(&User{}).
		Select("id", "username", "email").
		Where("id IN ?", ids).
		Find(&rows).Error; err != nil {
		common.SysError("failed to load aff rebate invitees: " + err.Error())
		return nil, errors.New("获取返利明细失败")
	}
	for _, row := range rows {
		identities[row.Id] = row
	}
	return identities, nil
}

// GetUserAffRebates returns one page of the rebates the given user earned as an
// inviter, newest first.
func GetUserAffRebates(inviterId int, pageInfo *common.PageInfo) (rebates []*AffRebate, total int64, err error) {
	if inviterId <= 0 {
		return nil, 0, errors.New("无效的用户 id")
	}
	query := DB.Model(&AffRebate{}).Where("inviter_id = ?", inviterId)
	if err = query.Count(&total).Error; err != nil {
		common.SysError("failed to count aff rebates: " + err.Error())
		return nil, 0, errors.New("获取返利明细失败")
	}
	if total == 0 {
		return []*AffRebate{}, 0, nil
	}
	if err = query.Order("id desc").
		Limit(pageInfo.GetPageSize()).
		Offset(pageInfo.GetStartIdx()).
		Find(&rebates).Error; err != nil {
		common.SysError("failed to list aff rebates: " + err.Error())
		return nil, 0, errors.New("获取返利明细失败")
	}
	return rebates, total, nil
}
