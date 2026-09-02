package model

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// UserOAuthBinding stores the binding relationship between users and custom OAuth providers
type UserOAuthBinding struct {
	Id             int       `json:"id" gorm:"primaryKey"`
	UserId         int       `json:"user_id" gorm:"not null;uniqueIndex:ux_user_provider"`                                    // User ID - one binding per user per provider
	ProviderId     int       `json:"provider_id" gorm:"not null;uniqueIndex:ux_user_provider;uniqueIndex:ux_provider_userid"` // Custom OAuth provider ID
	ProviderUserId string    `json:"provider_user_id" gorm:"type:varchar(256);not null;uniqueIndex:ux_provider_userid"`       // User ID from OAuth provider - one OAuth account per provider
	CreatedAt      time.Time `json:"created_at"`
}

// UserBindingStatus is a redacted view of a user's built-in identity
// bindings.  It deliberately contains booleans only; provider account IDs
// are stable identifiers and must not be sent to an administrator just to
// render binding state.
type UserBindingStatus struct {
	Email    bool `json:"email"`
	GitHub   bool `json:"github_id"`
	Discord  bool `json:"discord_id"`
	OIDC     bool `json:"oidc_id"`
	WeChat   bool `json:"wechat_id"`
	Telegram bool `json:"telegram_id"`
	LinuxDO  bool `json:"linux_do_id"`
}

func (UserOAuthBinding) TableName() string {
	return "user_oauth_bindings"
}

// GetUserBindingStatus returns only whether each built-in binding exists.
// The identity values are selected into a short-lived model object and are
// converted to booleans before leaving this package; callers cannot
// accidentally serialize the full User model.
func GetUserBindingStatus(userId int) (*UserBindingStatus, error) {
	if userId <= 0 {
		return nil, errors.New("user ID is required")
	}
	var user User
	if err := DB.Select(
		"email",
		"github_id",
		"discord_id",
		"oidc_id",
		"wechat_id",
		"telegram_id",
		"linux_do_id",
	).First(&user, "id = ?", userId).Error; err != nil {
		return nil, err
	}
	return &UserBindingStatus{
		Email:    user.Email != "",
		GitHub:   user.GitHubId != "",
		Discord:  user.DiscordId != "",
		OIDC:     user.OidcId != "",
		WeChat:   user.WeChatId != "",
		Telegram: user.TelegramId != "",
		LinuxDO:  user.LinuxDOId != "",
	}, nil
}

// GetUserOAuthBindingsByUserId returns all OAuth bindings for a user
func GetUserOAuthBindingsByUserId(userId int) ([]*UserOAuthBinding, error) {
	if userId <= 0 {
		return nil, errors.New("user ID is required")
	}
	var bindings []*UserOAuthBinding
	err := DB.Where("user_id = ?", userId).Find(&bindings).Error
	return bindings, err
}

// GetUserOAuthBinding returns a specific binding for a user and provider
func GetUserOAuthBinding(userId, providerId int) (*UserOAuthBinding, error) {
	if userId <= 0 || providerId <= 0 {
		return nil, errors.New("user and provider IDs are required")
	}
	var binding UserOAuthBinding
	err := DB.Where("user_id = ? AND provider_id = ?", userId, providerId).First(&binding).Error
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// GetUserByOAuthBinding finds a user by provider ID and provider user ID
func GetUserByOAuthBinding(providerId int, providerUserId string) (*User, error) {
	var binding UserOAuthBinding
	err := DB.Where("provider_id = ? AND provider_user_id = ?", providerId, providerUserId).First(&binding).Error
	if err != nil {
		return nil, err
	}

	var user User
	err = DB.First(&user, binding.UserId).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// IsProviderUserIdTaken checks if a provider user ID is already bound to any user
func IsProviderUserIdTaken(providerId int, providerUserId string) bool {
	var count int64
	if err := DB.Model(&UserOAuthBinding{}).
		Where("provider_id = ? AND provider_user_id = ?", providerId, providerUserId).
		Count(&count).Error; err != nil {
		common.SysLog("failed to check OAuth binding: " + err.Error())
		return false
	}
	return count > 0
}

// CreateUserOAuthBinding creates a new OAuth binding
func CreateUserOAuthBinding(binding *UserOAuthBinding) error {
	if binding == nil || binding.UserId <= 0 {
		return errors.New("user ID is required")
	}
	if binding.ProviderId <= 0 {
		return errors.New("provider ID is required")
	}
	if strings.TrimSpace(binding.ProviderUserId) == "" {
		return errors.New("provider user ID is required")
	}
	binding.ProviderUserId = strings.TrimSpace(binding.ProviderUserId)
	return DB.Transaction(func(tx *gorm.DB) error {
		return CreateUserOAuthBindingWithTx(tx, binding)
	})
}

// CreateUserOAuthBindingWithTx creates a new OAuth binding within a transaction
func CreateUserOAuthBindingWithTx(tx *gorm.DB, binding *UserOAuthBinding) error {
	if tx == nil {
		return errors.New("database transaction is required")
	}
	if binding == nil || binding.UserId <= 0 {
		return errors.New("user ID is required")
	}
	if binding.ProviderId <= 0 {
		return errors.New("provider ID is required")
	}
	if strings.TrimSpace(binding.ProviderUserId) == "" {
		return errors.New("provider user ID is required")
	}
	binding.ProviderUserId = strings.TrimSpace(binding.ProviderUserId)

	// Check if this provider user ID is already taken (use tx to check within the same transaction)
	var count int64
	if err := tx.Model(&UserOAuthBinding{}).
		Where("provider_id = ? AND provider_user_id = ?", binding.ProviderId, binding.ProviderUserId).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("this OAuth account is already bound to another user")
	}

	binding.CreatedAt = time.Now()
	return tx.Create(binding).Error
}

// UpdateUserOAuthBinding updates an existing OAuth binding (e.g., rebind to different OAuth account)
func UpdateUserOAuthBinding(userId, providerId int, newProviderUserId string) error {
	if userId <= 0 || providerId <= 0 || strings.TrimSpace(newProviderUserId) == "" {
		return errors.New("user, provider, and provider user IDs are required")
	}
	newProviderUserId = strings.TrimSpace(newProviderUserId)

	// Keep the conflict check and write in one transaction.  The unique
	// constraints remain the final arbiter, while propagating every lookup
	// error prevents a database outage from being mistaken for "no binding".
	return DB.Transaction(func(tx *gorm.DB) error {
		var existingBinding UserOAuthBinding
		err := tx.Where("provider_id = ? AND provider_user_id = ?", providerId, newProviderUserId).First(&existingBinding).Error
		if err == nil && existingBinding.UserId != userId {
			return errors.New("this OAuth account is already bound to another user")
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var binding UserOAuthBinding
		err = tx.Where("user_id = ? AND provider_id = ?", userId, providerId).First(&binding).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CreateUserOAuthBindingWithTx(tx, &UserOAuthBinding{
				UserId: userId, ProviderId: providerId, ProviderUserId: newProviderUserId,
			})
		}
		if err != nil {
			return err
		}

		result := tx.Model(&binding).Where("id = ? AND user_id = ? AND provider_id = ?", binding.Id, userId, providerId).
			Update("provider_user_id", newProviderUserId)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// MySQL may report zero for an unchanged value.  Confirm the row still
			// exists before treating the operation as successful.
			var count int64
			if err := tx.Model(&UserOAuthBinding{}).Where("id = ?", binding.Id).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		return nil
	})
}

// DeleteUserOAuthBinding deletes an OAuth binding
func DeleteUserOAuthBinding(userId, providerId int) error {
	if userId <= 0 || providerId <= 0 {
		return errors.New("user and provider IDs are required")
	}
	return DB.Where("user_id = ? AND provider_id = ?", userId, providerId).Delete(&UserOAuthBinding{}).Error
}

func deleteUserOAuthBindingsByUserId(tx *gorm.DB, userId int) error {
	return tx.Where("user_id = ?", userId).Delete(&UserOAuthBinding{}).Error
}

// GetBindingCountByProviderId returns the number of bindings for a provider
func GetBindingCountByProviderId(providerId int) (int64, error) {
	var count int64
	err := DB.Model(&UserOAuthBinding{}).Where("provider_id = ?", providerId).Count(&count).Error
	return count, err
}
