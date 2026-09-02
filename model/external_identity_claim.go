package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ExternalIdentityProviderGitHub   = "github"
	ExternalIdentityProviderDiscord  = "discord"
	ExternalIdentityProviderOIDC     = "oidc"
	ExternalIdentityProviderWeChat   = "wechat"
	ExternalIdentityProviderTelegram = "telegram"
	ExternalIdentityProviderLinuxDO  = "linuxdo"
)

// externalIdentityColumns is the compatibility map for built-in providers.
// Keep the users-table column names in one place so every write path uses the
// same durable claim key and startup backfill cannot silently omit a provider.
var externalIdentityColumns = []struct {
	provider string
	column   string
}{
	{ExternalIdentityProviderGitHub, "github_id"},
	{ExternalIdentityProviderDiscord, "discord_id"},
	{ExternalIdentityProviderOIDC, "oidc_id"},
	{ExternalIdentityProviderWeChat, "wechat_id"},
	{ExternalIdentityProviderTelegram, "telegram_id"},
	{ExternalIdentityProviderLinuxDO, "linux_do_id"},
}

// ExternalIdentityProviderForColumn returns the durable claim namespace for a
// built-in provider binding column. Custom OAuth providers store their IDs in
// user_oauth_bindings and therefore have no users-table column.
func ExternalIdentityProviderForColumn(column string) (string, bool) {
	column = strings.TrimSpace(column)
	for _, identity := range externalIdentityColumns {
		if identity.column == column {
			return identity.provider, true
		}
	}
	return "", false
}

var ErrExternalIdentityAlreadyClaimed = errors.New("external identity is already claimed")

// ExternalIdentityClaim is the durable ownership record for an identity issued
// by an external provider. The two unique indexes make both the provider
// subject and the user's provider slot single-owner without relying on a
// check-then-update sequence.
type ExternalIdentityClaim struct {
	Id        int64     `json:"id" gorm:"primaryKey"`
	Provider  string    `json:"provider" gorm:"type:varchar(32);not null;uniqueIndex:idx_external_identity_subject,priority:1;uniqueIndex:idx_external_identity_user,priority:1"`
	Subject   string    `json:"subject" gorm:"type:varchar(128);not null;uniqueIndex:idx_external_identity_subject,priority:2"`
	UserId    int       `json:"user_id" gorm:"not null;index;uniqueIndex:idx_external_identity_user,priority:2"`
	CreatedAt time.Time `json:"created_at"`
}

func (ExternalIdentityClaim) TableName() string {
	return "external_identity_claims"
}

// ClaimExternalIdentityWithTx atomically claims a provider subject for one
// user. Repeating the exact mapping is idempotent; every competing subject or
// user is rejected. Ownership is read back instead of trusting RowsAffected,
// whose duplicate-key semantics differ between supported databases.
func ClaimExternalIdentityWithTx(tx *gorm.DB, provider, subject string, userId int) error {
	provider = strings.TrimSpace(provider)
	subject = strings.TrimSpace(subject)
	if tx == nil || provider == "" || subject == "" || userId == 0 {
		return errors.New("external identity claim is invalid")
	}

	claim := ExternalIdentityClaim{Provider: provider, Subject: subject, UserId: userId}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim)
	if result.Error != nil {
		return result.Error
	}
	var subjectOwner ExternalIdentityClaim
	if err := tx.Where("provider = ? AND subject = ?", provider, subject).First(&subjectOwner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrExternalIdentityAlreadyClaimed
		}
		return err
	}
	if subjectOwner.UserId != userId {
		return ErrExternalIdentityAlreadyClaimed
	}

	var userClaim ExternalIdentityClaim
	if err := tx.Where("provider = ? AND user_id = ?", provider, userId).First(&userClaim).Error; err != nil {
		return err
	}
	if userClaim.Subject != subject {
		return ErrExternalIdentityAlreadyClaimed
	}
	return nil
}

func ReleaseExternalIdentityWithTx(tx *gorm.DB, provider string, userId int) error {
	provider = strings.TrimSpace(provider)
	if tx == nil || provider == "" || userId == 0 {
		return errors.New("external identity release is invalid")
	}
	return tx.Where("provider = ? AND user_id = ?", provider, userId).
		Delete(&ExternalIdentityClaim{}).Error
}

func releaseAllExternalIdentitiesWithTx(tx *gorm.DB, userId int) error {
	if tx == nil || userId == 0 {
		return errors.New("external identity release is invalid")
	}
	return tx.Where("user_id = ?", userId).Delete(&ExternalIdentityClaim{}).Error
}

// InitializeExternalIdentityClaims imports legacy built-in provider bindings
// after the claim table is migrated. Existing duplicate ownership fails
// migration rather than preserving an ambiguous login identity.
func InitializeExternalIdentityClaims() error {
	selectColumns := []string{"id"}
	identityPredicates := make([]string, 0, len(externalIdentityColumns))
	for _, identity := range externalIdentityColumns {
		selectColumns = append(selectColumns, identity.column)
		// COALESCE handles legacy nullable columns consistently across all
		// supported SQL dialects.
		identityPredicates = append(identityPredicates, fmt.Sprintf("COALESCE(%s, '') <> ''", identity.column))
	}
	var users []User
	if err := DB.Unscoped().Select(selectColumns).
		Where(strings.Join(identityPredicates, " OR ")).
		Order("id ASC").Find(&users).Error; err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		for _, user := range users {
			values := map[string]string{
				"github_id":   user.GitHubId,
				"discord_id":  user.DiscordId,
				"oidc_id":     user.OidcId,
				"wechat_id":   user.WeChatId,
				"telegram_id": user.TelegramId,
				"linux_do_id": user.LinuxDOId,
			}
			for _, identity := range externalIdentityColumns {
				if subject := strings.TrimSpace(values[identity.column]); subject != "" {
					if err := ClaimExternalIdentityWithTx(tx, identity.provider, subject, user.Id); err != nil {
						return fmt.Errorf("backfill %s identity for user %d: %w", identity.provider, user.Id, err)
					}
				}
			}
		}
		return nil
	})
}
