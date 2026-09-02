package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const UserNameMaxLength = 20

// userListCountHardLimit bounds the work used to populate administrator user
// list/search totals.  A capped total is preferable to an unbounded aggregate
// COUNT on a table that is directly reachable from an administrative endpoint.
// Keep this aligned with the other administrative list endpoints, which all
// expose a 10,000-row upper bound for their totals.
const userListCountHardLimit = 10000

var userSortColumns = map[string]string{
	"id":            "id",
	"username":      "username",
	"quota":         "quota",
	"group":         "group",
	"created_at":    "created_at",
	"last_login_at": "last_login_at",
}

type UserSortOptions struct {
	SortBy    string
	SortOrder string
}

// UserListItem is the intentionally small projection returned by the admin
// user list/search endpoints.  Do not add authentication, identity-binding,
// payment, or notification-setting fields here: those values either contain
// credentials or are only needed by the user's own profile/detail flows.
// Keeping a separate projection (rather than serializing User and omitting a
// few fields) makes adding a field to User unable to accidentally widen this
// response.
type UserListItem struct {
	Id              int            `json:"id"`
	Username        string         `json:"username"`
	DisplayName     string         `json:"display_name"`
	Role            int            `json:"role"`
	Status          int            `json:"status"`
	Quota           int            `json:"quota"`
	UsedQuota       int            `json:"used_quota"`
	RequestCount    int            `json:"request_count"`
	Group           string         `json:"group"`
	AffCount        int            `json:"aff_count"`
	AffQuota        int            `json:"aff_quota"`
	AffHistoryQuota int            `json:"aff_history_quota" gorm:"column:aff_history"`
	InviterId       int            `json:"inviter_id"`
	Remark          string         `json:"remark,omitempty"`
	CreatedAt       int64          `json:"created_at"`
	LastLoginAt     int64          `json:"last_login_at"`
	DeletedAt       gorm.DeletedAt `json:"DeletedAt,omitempty"`
}

// userListSelectFields is expressed using User model field names.  GORM maps
// those names to the correct dialect-specific column names (including the
// reserved group column and the aff_history column).
var userListSelectFields = []string{
	"Id",
	"Username",
	"DisplayName",
	"Role",
	"Status",
	"Quota",
	"UsedQuota",
	"RequestCount",
	"Group",
	"AffCount",
	"AffQuota",
	"AffHistoryQuota",
	"InviterId",
	"Remark",
	"CreatedAt",
	"LastLoginAt",
	"DeletedAt",
}

// AdminUserDetail is the explicit projection returned by the administrator
// user-detail endpoint.  Keep this type deliberately separate from User:
// User contains authentication credentials, OAuth identities, notification
// settings, payment identifiers, and other fields that must not be returned
// to an administrator merely because a new field was added to the model.
type AdminUserDetail struct {
	Id               int                        `json:"id"`
	Username         string                     `json:"username"`
	DisplayName      string                     `json:"display_name"`
	Role             int                        `json:"role"`
	Status           int                        `json:"status"`
	Quota            int                        `json:"quota"`
	UsedQuota        int                        `json:"used_quota"`
	RequestCount     int                        `json:"request_count"`
	Group            string                     `json:"group"`
	AffCount         int                        `json:"aff_count"`
	AffQuota         int                        `json:"aff_quota"`
	AffHistoryQuota  int                        `json:"aff_history_quota"`
	InviterId        int                        `json:"inviter_id"`
	Remark           string                     `json:"remark,omitempty"`
	CreatedAt        int64                      `json:"created_at"`
	LastLoginAt      int64                      `json:"last_login_at"`
	DeletedAt        gorm.DeletedAt             `json:"DeletedAt,omitempty"`
	AdminPermissions map[string]map[string]bool `json:"admin_permissions,omitempty"`
}

// userAdminDetailSelectFields is deliberately kept separate from User's
// default select.  GetUserById(false) is used by several internal flows and
// historically loaded every non-password column; the admin detail endpoint
// must never rely on that broad model serialization.  Keep this projection in
// lockstep with AdminUserDetail's public, non-secret fields only.
var userAdminDetailSelectFields = []string{
	"Id",
	"Username",
	"DisplayName",
	"Role",
	"Status",
	"Quota",
	"UsedQuota",
	"RequestCount",
	"Group",
	"AffCount",
	"AffQuota",
	"AffHistoryQuota",
	"InviterId",
	"Remark",
	"CreatedAt",
	"LastLoginAt",
	"DeletedAt",
}

func NewUserSortOptions(sortBy string, sortOrder string) UserSortOptions {
	normalizedSortBy := strings.ToLower(strings.TrimSpace(sortBy))
	normalizedSortOrder := strings.ToLower(strings.TrimSpace(sortOrder))
	if _, ok := userSortColumns[normalizedSortBy]; !ok {
		normalizedSortBy = "id"
		normalizedSortOrder = "desc"
	} else if normalizedSortOrder != "asc" {
		normalizedSortOrder = "desc"
	}

	return UserSortOptions{
		SortBy:    normalizedSortBy,
		SortOrder: normalizedSortOrder,
	}
}

func (options UserSortOptions) Apply(query *gorm.DB) *gorm.DB {
	columnName, ok := userSortColumns[options.SortBy]
	if !ok {
		columnName = "id"
	}
	q := query.Order(clause.OrderByColumn{
		Column: clause.Column{Name: columnName},
		Desc:   options.SortOrder != "asc",
	})
	if columnName != "id" {
		q = q.Order(clause.OrderByColumn{
			Column: clause.Column{Name: "id"},
			Desc:   true,
		})
	}
	return q
}

func resolveUserSortOptions(sortOptions []UserSortOptions) UserSortOptions {
	if len(sortOptions) == 0 {
		return NewUserSortOptions("", "")
	}
	return sortOptions[0]
}

// User if you add sensitive fields, don't forget to clean them in setupLogin function.
// Otherwise, the sensitive information will be saved on local storage in plain text!
type User struct {
	Id               int    `json:"id"`
	Username         string `json:"username" gorm:"unique;index" validate:"max=20"`
	Password         string `json:"password" gorm:"not null;" validate:"min=8,max=20"`
	OriginalPassword string `json:"original_password" gorm:"-:all"` // this field is only for Password change verification, don't save it to database!
	DisplayName      string `json:"display_name" gorm:"index" validate:"max=20"`
	Role             int    `json:"role" gorm:"type:int;default:1"`   // admin, common
	Status           int    `json:"status" gorm:"type:int;default:1"` // enabled, disabled
	Email            string `json:"email" gorm:"index" validate:"max=50"`
	GitHubId         string `json:"github_id" gorm:"column:github_id;index"`
	DiscordId        string `json:"discord_id" gorm:"column:discord_id;index"`
	OidcId           string `json:"oidc_id" gorm:"column:oidc_id;index"`
	WeChatId         string `json:"wechat_id" gorm:"column:wechat_id;index"`
	TelegramId       string `json:"telegram_id" gorm:"column:telegram_id;index"`
	VerificationCode string `json:"verification_code" gorm:"-:all"` // this field is only for Email verification, don't save it to database!
	// AccessToken is a runtime-only plaintext PAT. The historical physical
	// column is retained as a deterministic fingerprint marker (fp:v1:<hmac>)
	// so its uniqueness fence remains useful without keeping the bearer token
	// in the database. AccessTokenCiphertext hydrates this field after a query;
	// AccessTokenHash is the indexed lookup value.
	AccessToken           *string                    `json:"-" gorm:"-:all"` // runtime plaintext; never mapped to a database column
	LegacyAccessToken     *string                    `json:"-" gorm:"type:varchar(128);column:access_token;uniqueIndex:idx_users_access_token"`
	AccessTokenCiphertext string                     `json:"-" gorm:"column:access_token_ciphertext;type:text"`
	AccessTokenHash       *string                    `json:"-" gorm:"column:access_token_hash;type:char(64);index"`
	Quota                 int                        `json:"quota" gorm:"type:bigint;default:0"`
	UsedQuota             int                        `json:"used_quota" gorm:"type:bigint;default:0;column:used_quota"` // used quota
	RequestCount          int                        `json:"request_count" gorm:"type:int;default:0;"`                  // request number
	Group                 string                     `json:"group" gorm:"type:varchar(64);default:'default'"`
	AffCode               string                     `json:"aff_code" gorm:"type:varchar(32);column:aff_code;uniqueIndex"`
	AffCount              int                        `json:"aff_count" gorm:"type:int;default:0;column:aff_count"`
	AffQuota              int                        `json:"aff_quota" gorm:"type:bigint;default:0;column:aff_quota"`           // 邀请剩余额度
	AffHistoryQuota       int                        `json:"aff_history_quota" gorm:"type:bigint;default:0;column:aff_history"` // 邀请历史额度
	InviterId             int                        `json:"inviter_id" gorm:"type:int;column:inviter_id;index"`
	DeletedAt             gorm.DeletedAt             `gorm:"index"`
	LinuxDOId             string                     `json:"linux_do_id" gorm:"column:linux_do_id;index"`
	Setting               string                     `json:"setting" gorm:"type:text;column:setting"`
	Remark                string                     `json:"remark,omitempty" gorm:"type:varchar(255)" validate:"max=255"`
	StripeCustomer        string                     `json:"stripe_customer" gorm:"type:varchar(64);column:stripe_customer;index"`
	CreatedAt             int64                      `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	LastLoginAt           int64                      `json:"last_login_at" gorm:"default:0;column:last_login_at"`
	AuthVersion           int64                      `json:"-" gorm:"type:bigint;not null;default:1;column:auth_version"`
	AdminPermissions      map[string]map[string]bool `json:"admin_permissions,omitempty" gorm:"-:all"`

	creationQuotaForNewUser int
	creationQuotaForInvitee int
	creationQuotaForInviter int
	creationInviterId       int
}

func (user *User) ToBaseUser() *UserBase {
	cache := &UserBase{
		Id:          user.Id,
		Group:       user.Group,
		Quota:       user.Quota,
		Status:      user.Status,
		Role:        user.Role,
		Username:    user.Username,
		Setting:     user.Setting,
		Email:       user.Email,
		AuthVersion: user.AuthVersion,
		CacheSchema: userCacheSchemaVersion,
	}
	return cache
}

func (user *User) GetAccessToken() string {
	if user.AccessToken == nil {
		return ""
	}
	return *user.AccessToken
}

func (user *User) SetAccessToken(token string) {
	user.AccessToken = &token
}

// UpdateUserAccessToken rotates a dashboard personal access token without
// writing a stale user snapshot back over concurrently updated fields.
func UpdateUserAccessToken(id int, token string) error {
	if id <= 0 {
		return errors.New("id 为空！")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	marker, ciphertext, hash, err := sealCredential(token)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{
		"access_token_ciphertext": ciphertext,
	}
	if marker == "" {
		updates["access_token"] = nil
		updates["access_token_hash"] = nil
	} else {
		updates["access_token"] = marker
		updates["access_token_hash"] = hash
	}
	result := DB.Session(&gorm.Session{SkipHooks: true}).Model(&User{}).
		Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (user *User) GetSetting() dto.UserSetting {
	setting, err := user.GetSettingWithError()
	if err != nil {
		common.SysLog("failed to read user setting: " + err.Error())
	}
	return setting
}

func (user *User) GetSettingWithError() (dto.UserSetting, error) {
	if user == nil {
		return dto.UserSetting{}, nil
	}
	return unmarshalUserSettingFromStorage(user.Setting)
}

func (user *User) SetSetting(setting dto.UserSetting) {
	settingValue, err := marshalUserSettingForStorage(setting)
	if err != nil {
		common.SysLog("failed to store user setting: " + err.Error())
		return
	}
	user.Setting = settingValue
}

func UpdateUserSetting(userId int, setting dto.UserSetting) error {
	if userId == 0 {
		return errors.New("id 为空！")
	}
	settingValue, err := marshalUserSettingForStorage(setting)
	if err != nil {
		return err
	}
	result := DB.Model(&User{}).Where("id = ?", userId).Update("setting", settingValue)
	if err = result.Error; err != nil {
		return err
	}
	if result.RowsAffected == 0 {
		// Drivers such as MySQL may report zero for an unchanged value.  Verify
		// that the account still exists before publishing a cache entry; a
		// missing/soft-deleted user must never acquire a resurrected setting.
		var count int64
		if err := DB.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	return updateUserSettingCache(userId, settingValue)
}

// userBindColumns 允许通过 UpdateUserBindColumn 更新的第三方账号绑定列白名单。
// 列名只可能来自代码内部的 provider 实现，白名单是防御纵深，不依赖调用方自律。
var userBindColumns = map[string]bool{
	"github_id":   true,
	"discord_id":  true,
	"oidc_id":     true,
	"linux_do_id": true,
	"wechat_id":   true,
}

// UpdateUserBindColumn 第三方账号绑定字段的专用更新。
// 绑定操作必须只写绑定列：若改为“读取完整用户 → 改一个字段 → 整体更新”，
// 读快照期间并发发生的封禁、降权或分组变更会被旧快照覆盖恢复。
// 角色、状态、分组只允许通过各自带锁/CAS 的专用方法修改。
func UpdateUserBindColumn(userId int, column string, value string) error {
	if userId <= 0 {
		return errors.New("id 为空！")
	}
	if !userBindColumns[column] {
		return fmt.Errorf("invalid user bind column: %s", column)
	}
	result := DB.Model(&User{}).Where("id = ?", userId).Update(column, value)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// A no-op update is valid when the value is unchanged, but a missing
		// account must be reported instead of silently claiming success.
		var count int64
		if err := DB.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	return nil
}

// UpdateUserBindColumnWithTx is the transaction-aware counterpart used when
// the durable external-identity claim and users-table binding must commit as
// one operation. It shares the same whitelist as UpdateUserBindColumn.
func UpdateUserBindColumnWithTx(tx *gorm.DB, userId int, column, value string) error {
	if tx == nil {
		return errors.New("database transaction is nil")
	}
	if userId <= 0 {
		return errors.New("id 为空！")
	}
	if !userBindColumns[column] {
		return fmt.Errorf("invalid user bind column: %s", column)
	}
	result := tx.Model(&User{}).Where("id = ?", userId).Update(column, value)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// MySQL may report zero when the value is unchanged. Distinguish that
		// harmless case from a missing/soft-deleted user before allowing the
		// surrounding identity claim to commit.
		var count int64
		if err := tx.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	return nil
}

// 根据用户角色生成默认的边栏配置
func generateDefaultSidebarConfigForRole(userRole int) string {
	defaultConfig := map[string]interface{}{}

	// 聊天区域 - 所有用户都可以访问
	defaultConfig["chat"] = map[string]interface{}{
		"enabled":    true,
		"playground": true,
		"chat":       true,
	}

	// 控制台区域 - 所有用户都可以访问
	defaultConfig["console"] = map[string]interface{}{
		"enabled":    true,
		"detail":     true,
		"token":      true,
		"log":        true,
		"midjourney": true,
		"task":       true,
	}

	// 个人中心区域 - 所有用户都可以访问
	defaultConfig["personal"] = map[string]interface{}{
		"enabled":  true,
		"topup":    true,
		"ticket":   true,
		"personal": true,
	}

	// 管理员区域 - 根据角色决定
	if userRole == common.RoleAdminUser {
		// 管理员可以访问管理员区域，但不能访问系统设置
		defaultConfig["admin"] = map[string]interface{}{
			"enabled":    true,
			"channel":    true,
			"models":     true,
			"redemption": true,
			"user":       true,
			"setting":    false, // 管理员不能访问系统设置
		}
	} else if userRole == common.RoleRootUser {
		// 超级管理员可以访问所有功能
		defaultConfig["admin"] = map[string]interface{}{
			"enabled":    true,
			"channel":    true,
			"models":     true,
			"redemption": true,
			"user":       true,
			"setting":    true,
		}
	}
	// 普通用户不包含admin区域

	// 转换为JSON字符串
	configBytes, err := common.Marshal(defaultConfig)
	if err != nil {
		common.SysLog("生成默认边栏配置失败: " + err.Error())
		return ""
	}

	return string(configBytes)
}

// CheckUserExistOrDeleted check if user exist or deleted, if not exist, return false, nil, if deleted or exist, return true, nil
func CheckUserExistOrDeleted(username string, email string) (bool, error) {
	var user User

	// err := DB.Unscoped().First(&user, "username = ? or email = ?", username, email).Error
	// check email if empty
	var err error
	email = NormalizeEmail(email)
	if email == "" {
		err = DB.Unscoped().First(&user, "username = ?", username).Error
	} else {
		err = DB.Unscoped().First(&user, "username = ? or LOWER(email) = ?", username, email).Error
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// not exist, return false, nil
			return false, nil
		}
		// other error, return false, err
		return false, err
	}
	// exist, return true, nil
	return true, nil
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func emailQuery(tx *gorm.DB, email string) *gorm.DB {
	if tx == nil {
		tx = DB
	}
	return tx.Unscoped().Model(&User{}).Where("LOWER(email) = ?", NormalizeEmail(email))
}

func CountUsersByEmail(email string) (int64, error) {
	email = NormalizeEmail(email)
	if email == "" {
		return 0, nil
	}
	var count int64
	err := emailQuery(DB, email).Count(&count).Error
	return count, err
}

func IsEmailAvailable(email string, excludeUserID int) (bool, error) {
	email = NormalizeEmail(email)
	if email == "" {
		return true, nil
	}
	query := emailQuery(DB, email)
	if excludeUserID > 0 {
		query = query.Where("id <> ?", excludeUserID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count == 0, nil
}

func EnsureEmailAvailable(email string, excludeUserID int) error {
	available, err := IsEmailAvailable(email, excludeUserID)
	if err != nil {
		return err
	}
	if !available {
		return ErrEmailAlreadyTaken
	}
	return nil
}

// withNormalizedEmailLock serializes concurrent writers that target the same
// normalized email inside tx, so a "check then write" sequence cannot be raced
// by two transactions. It must be called inside an active transaction; the lock
// is scoped to that transaction and released on commit/rollback.
//
//   - PostgreSQL: transaction-level advisory lock keyed by the normalized email.
//   - MySQL (default REPEATABLE READ): a locking read that takes a next-key/gap
//     lock on the email index, blocking concurrent inserts of the same value.
//   - SQLite: no explicit lock; the single-writer model already serializes the
//     write, so a racing second write fails instead of duplicating.
//
// An empty email is allowed to repeat and needs no serialization.
func withNormalizedEmailLock(tx *gorm.DB, email string, fn func(tx *gorm.DB) error) error {
	email = NormalizeEmail(email)
	if email == "" {
		return fn(tx)
	}
	switch {
	case common.UsingMainDatabase(common.DatabaseTypePostgreSQL):
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", email).Error; err != nil {
			return err
		}
	case common.UsingMainDatabase(common.DatabaseTypeMySQL):
		var ids []int
		if err := tx.Raw("SELECT id FROM users WHERE email = ? FOR UPDATE", email).Scan(&ids).Error; err != nil {
			return err
		}
	}
	return fn(tx)
}

func GetMaxUserId() int {
	var user User
	DB.Unscoped().Last(&user)
	return user.Id
}

func GetAllUsers(pageInfo *common.PageInfo, sortOptions ...UserSortOptions) (users []*User, total int64, err error) {
	// Start transaction
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Get total count within transaction
	err = tx.Unscoped().Model(&User{}).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated users within same transaction
	order := resolveUserSortOptions(sortOptions)
	err = order.Apply(tx.Unscoped()).Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).
		Omit("password", "access_token", "access_token_ciphertext", "access_token_hash").Find(&users).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Commit transaction
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

// applyUserListVisibility restricts an admin list to users below the
// operator's role.  Root is the sole exception and may see every role.  The
// fail-closed branch is useful as defence in depth if a caller ever bypasses
// AdminAuth (or passes a malformed role value).
func applyUserListVisibility(query *gorm.DB, actorRole int) *gorm.DB {
	if actorRole == common.RoleRootUser {
		return query
	}
	if !common.IsValidateRole(actorRole) || actorRole < common.RoleAdminUser {
		return query.Where("1 = 0")
	}
	return query.Where("role < ?", actorRole)
}

// GetAllUsersForRole returns the safe admin-list projection visible to an
// operator.  It deliberately does not return []*User: User contains OAuth
// identifiers, notification credentials, payment identifiers, and other
// fields that must never be serialized by a list endpoint.
func GetAllUsersForRole(pageInfo *common.PageInfo, actorRole int, sortOptions ...UserSortOptions) (users []*UserListItem, total int64, err error) {
	if pageInfo == nil {
		return nil, 0, errors.New("page info is nil")
	}
	if DB == nil {
		return nil, 0, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	pageSize := pageInfo.GetPageSize()
	if pageSize <= 0 {
		pageSize = common.ItemsPerPage
		if pageSize <= 0 {
			pageSize = 1
		}
	}
	if pageSize > common.MaxPageSize {
		pageSize = common.MaxPageSize
	}
	startIdx := pageInfo.GetStartIdx()
	// The reported total is deliberately capped below.  Keep offsets within
	// the same bounded window so a hostile page number cannot force an
	// unbounded OFFSET scan even though the response would be empty.
	if startIdx > userListCountHardLimit {
		startIdx = userListCountHardLimit
	}
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			users = nil
			total = 0
			err = fmt.Errorf("%w: user list query failed", ErrDatabase)
		}
	}()

	query := applyUserListVisibility(tx.Unscoped().Model(&User{}), actorRole)
	count, countErr := boundedPrimaryKeyCount(query, userListCountHardLimit)
	if countErr != nil {
		tx.Rollback()
		return nil, 0, countErr
	}
	total = count

	order := resolveUserSortOptions(sortOptions)
	err = order.Apply(query.Select(userListSelectFields)).
		Limit(pageSize).Offset(startIdx).Find(&users).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

func SearchUsers(keyword string, group string, role *int, status *int, startIdx int, num int, sortOptions ...UserSortOptions) ([]*User, int64, error) {
	var users []*User
	var total int64
	var err error

	// 开始事务
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 构建基础查询
	query := tx.Unscoped().Model(&User{})

	// 构建搜索条件
	likeCondition := "username LIKE ? OR email LIKE ? OR display_name LIKE ?"
	likeArgs := []interface{}{"%" + keyword + "%", "%" + keyword + "%", "%" + keyword + "%"}

	// 尝试将关键字转换为整数ID
	keywordInt, err := strconv.Atoi(keyword)
	if err == nil {
		// 如果是数字，同时搜索ID和其他字段
		likeCondition = "id = ? OR " + likeCondition
		likeArgs = append([]interface{}{keywordInt}, likeArgs...)
	}

	query = query.Where("("+likeCondition+")", likeArgs...)
	if group != "" {
		query = query.Where(commonGroupCol+" = ?", group)
	}
	if role != nil {
		query = query.Where("role = ?", *role)
	}
	if status != nil {
		if *status == -1 {
			query = query.Where("deleted_at IS NOT NULL")
		} else {
			query = query.Where("deleted_at IS NULL").Where("status = ?", *status)
		}
	}

	// 获取总数
	err = query.Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 获取分页数据
	order := resolveUserSortOptions(sortOptions)
	err = order.Apply(query.Omit("password", "access_token", "access_token_ciphertext", "access_token_hash")).
		Limit(num).Offset(startIdx).Find(&users).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 提交事务
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

// SearchUsersForRole is the role-scoped counterpart of SearchUsers.  Search
// filters are applied in addition to (never instead of) the operator
// visibility predicate, so an admin cannot recover a same-level/root row by
// searching for its exact id, email, or username.
func SearchUsersForRole(keyword string, group string, role *int, status *int, actorRole int, startIdx int, num int, sortOptions ...UserSortOptions) (users []*UserListItem, total int64, err error) {
	if DB == nil {
		return nil, 0, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx > userListCountHardLimit {
		startIdx = userListCountHardLimit
	}
	if num <= 0 {
		num = common.ItemsPerPage
		if num <= 0 {
			num = 1
		}
	}
	if num > common.MaxPageSize {
		num = common.MaxPageSize
	}

	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			users = nil
			total = 0
			err = fmt.Errorf("%w: user search query failed", ErrDatabase)
		}
	}()

	query := applyUserListVisibility(tx.Unscoped().Model(&User{}), actorRole)
	likeCondition := "username LIKE ? OR email LIKE ? OR display_name LIKE ?"
	likeArgs := []interface{}{"%" + keyword + "%", "%" + keyword + "%", "%" + keyword + "%"}
	if keywordInt, err := strconv.Atoi(keyword); err == nil {
		likeCondition = "id = ? OR " + likeCondition
		likeArgs = append([]interface{}{keywordInt}, likeArgs...)
	}
	query = query.Where("("+likeCondition+")", likeArgs...)
	if group != "" {
		query = query.Where(commonGroupCol+" = ?", group)
	}
	if role != nil {
		query = query.Where("role = ?", *role)
	}
	if status != nil {
		if *status == -1 {
			query = query.Where("deleted_at IS NOT NULL")
		} else {
			query = query.Where("deleted_at IS NULL").Where("status = ?", *status)
		}
	}

	total, err = boundedPrimaryKeyCount(query, userListCountHardLimit)
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	order := resolveUserSortOptions(sortOptions)
	if err = order.Apply(query.Select(userListSelectFields)).Limit(num).Offset(startIdx).Find(&users).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

func GetUserById(id int, selectAll bool) (*User, error) {
	if id == 0 {
		return nil, errors.New("id 为空！")
	}
	user := User{Id: id}
	var err error = nil
	if selectAll {
		err = DB.First(&user, "id = ?", id).Error
	} else {
		err = DB.Omit("password", "access_token", "access_token_ciphertext", "access_token_hash").
			First(&user, "id = ?", id).Error
	}
	return &user, err
}

// GetUserByIdForAdmin loads only the fields needed by the administrator user
// detail view.  In particular, it does not select password, access_token,
// OAuth identities, Stripe identifiers, or the JSON setting (which may hold
// webhook/Gotify credentials).  Callers must still enforce the target-role
// policy before returning the object to an administrator.
// ToAdminUserDetail converts a loaded User into the explicit administrator
// projection.  It is intentionally a field-by-field copy: serializing User
// directly would re-introduce any sensitive fields added in the future.
func (user *User) ToAdminUserDetail() *AdminUserDetail {
	if user == nil {
		return nil
	}
	return &AdminUserDetail{
		Id:               user.Id,
		Username:         user.Username,
		DisplayName:      user.DisplayName,
		Role:             user.Role,
		Status:           user.Status,
		Quota:            user.Quota,
		UsedQuota:        user.UsedQuota,
		RequestCount:     user.RequestCount,
		Group:            user.Group,
		AffCount:         user.AffCount,
		AffQuota:         user.AffQuota,
		AffHistoryQuota:  user.AffHistoryQuota,
		InviterId:        user.InviterId,
		Remark:           user.Remark,
		CreatedAt:        user.CreatedAt,
		LastLoginAt:      user.LastLoginAt,
		DeletedAt:        user.DeletedAt,
		AdminPermissions: user.AdminPermissions,
	}
}

func GetUserByIdForAdmin(id int) (*AdminUserDetail, error) {
	if id <= 0 {
		return nil, errors.New("id 为空！")
	}
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	user := User{Id: id}
	if err := DB.Select(userAdminDetailSelectFields).First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return user.ToAdminUserDetail(), nil
}

func GetUserIdByAffCode(affCode string) (int, error) {
	if affCode == "" {
		return 0, errors.New("affCode 为空！")
	}
	var user User
	err := DB.Select("id").First(&user, "aff_code = ?", affCode).Error
	return user.Id, err
}

func DeleteUserById(id int) (err error) {
	if id == 0 {
		return errors.New("id 为空！")
	}
	user := User{Id: id}
	return user.Delete()
}

func HardDeleteUserById(id int) error {
	if id == 0 {
		return errors.New("id 为空！")
	}
	user := User{Id: id}
	return user.HardDelete()
}

func (user *User) TransferAffQuotaToQuota(quota int) error {
	// 检查quota是否小于最小额度
	quotaPerUnit := common.GetQuotaPerUnit()
	if float64(quota) < quotaPerUnit {
		return fmt.Errorf("转移额度最小为%s！", logger.LogQuota(common.QuotaFromFloat(quotaPerUnit)))
	}

	// 开始数据库事务
	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() // 确保在函数退出时事务能回滚

	// 加锁查询用户以确保数据一致性
	err := lockForUpdate(tx).First(user, user.Id).Error
	if err != nil {
		return err
	}

	// 再次检查用户的AffQuota是否足够
	if user.AffQuota < quota {
		return errors.New("邀请额度不足！")
	}

	// 更新用户额度
	user.AffQuota -= quota
	user.Quota += quota

	// Only the two wallet columns belong to this operation. A full Save can
	// overwrite a concurrently rotated management PAT with the loaded snapshot.
	result := tx.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"aff_quota": user.AffQuota,
		"quota":     user.Quota,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	if _, err := stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, user.Id, getUserCacheKey(user.Id)); err != nil {
		return err
	}

	// 提交事务
	if err := tx.Commit().Error; err != nil {
		return err
	}
	syncCreditUserQuotaCache(user.Id, quota, "affiliate transfer")
	return nil
}

func (user *User) prepareForInsert(tx *gorm.DB) error {
	user.Email = NormalizeEmail(user.Email)
	if err := ensureEmailAvailableWithTx(tx, user.Email, 0); err != nil {
		return err
	}
	if user.Password == "" {
		return nil
	}
	var err error
	user.Password, err = common.Password2Hash(user.Password)
	return err
}

// createWithRegistrationRewardsTx persists the account and both sides of an
// affiliate reward in one transaction. The transient snapshot is populated
// only after every ledger write succeeds, so post-commit logging cannot claim
// that a rolled-back reward was granted.
func (user *User) createWithRegistrationRewardsTx(tx *gorm.DB, inviterId int, runtimeConfig common.GeneralRuntimeConfig) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}
	if inviterId < 0 {
		return errors.New("invalid inviter id")
	}
	user.creationQuotaForNewUser = 0
	user.creationQuotaForInvitee = 0
	user.creationQuotaForInviter = 0
	user.creationInviterId = 0

	newUserQuota := runtimeConfig.QuotaForNewUser
	if newUserQuota < 0 {
		return errors.New("new-user quota cannot be negative")
	}
	if err := common.ValidateWalletQuota(newUserQuota); err != nil {
		return err
	}
	inviteeQuota := 0
	inviterQuota := 0
	if inviterId > 0 {
		var inviter User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", inviterId).First(&inviter).Error; err != nil {
			return err
		}
	}
	if inviterId > 0 && operation_setting.IsPaymentComplianceConfirmed() {
		inviteeQuota = runtimeConfig.QuotaForInvitee
		inviterQuota = runtimeConfig.QuotaForInviter
		if inviteeQuota < 0 || inviterQuota < 0 {
			return errors.New("affiliate reward quota cannot be negative")
		}
		if err := common.ValidateWalletQuota(inviteeQuota); err != nil {
			return err
		}
		if err := common.ValidateWalletQuota(inviterQuota); err != nil {
			return err
		}
		if newUserQuota > common.MaxWalletQuota-inviteeQuota {
			return ErrWalletQuotaLimitExceeded
		}
	}

	user.Quota = newUserQuota + inviteeQuota
	user.InviterId = inviterId
	if err := tx.Create(user).Error; err != nil {
		return err
	}
	if inviterQuota > 0 {
		result := tx.Model(&User{}).
			Where("id = ? AND aff_count >= 0 AND aff_count < ? AND aff_quota >= 0 AND aff_quota <= ? AND aff_history >= 0 AND aff_history <= ?",
				inviterId, common.MaxQuota, common.MaxWalletQuota-inviterQuota, common.MaxWalletQuota-inviterQuota).
			Updates(map[string]interface{}{
				"aff_count":   gorm.Expr("aff_count + ?", 1),
				"aff_quota":   gorm.Expr("aff_quota + ?", inviterQuota),
				"aff_history": gorm.Expr("aff_history + ?", inviterQuota),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var count int64
			if err := tx.Model(&User{}).Where("id = ?", inviterId).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
			return fmt.Errorf("affiliate reward ledger exceeds supported range for inviter %d", inviterId)
		}
	}

	user.creationQuotaForNewUser = newUserQuota
	user.creationQuotaForInvitee = inviteeQuota
	user.creationQuotaForInviter = inviterQuota
	user.creationInviterId = inviterId
	return nil
}

// BindEmailToUser atomically checks email availability and assigns it to the
// user, serializing concurrent binds of the same email so two accounts cannot
// end up sharing one address. The email is normalized before check and store.
func BindEmailToUser(user *User, email string) error {
	email = NormalizeEmail(email)
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return withNormalizedEmailLock(tx, email, func(tx *gorm.DB) error {
			if err := ensureEmailAvailableWithTx(tx, email, user.Id); err != nil {
				return err
			}
			user.Email = email
			return user.UpdateWithTx(tx, false)
		})
	}); err != nil {
		return err
	}
	return updateUserCache(*user)
}

func ensureEmailAvailableWithTx(tx *gorm.DB, email string, excludeUserID int) error {
	email = NormalizeEmail(email)
	if email == "" {
		return nil
	}
	query := emailQuery(tx, email)
	if excludeUserID > 0 {
		query = query.Where("id <> ?", excludeUserID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrEmailAlreadyTaken
	}
	return nil
}

func (user *User) Insert(inviterId int) error {
	runtimeConfig := common.GetGeneralRuntimeConfig()
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return withNormalizedEmailLock(tx, user.Email, func(tx *gorm.DB) error {
			if err := user.prepareForInsert(tx); err != nil {
				return err
			}
			user.AffCode = common.GetRandomString(4)

			// 初始化用户设置，包括默认的边栏配置
			if user.Setting == "" {
				defaultSetting := dto.UserSetting{}
				// 这里暂时不设置SidebarModules，因为需要在用户创建后根据角色设置
				user.SetSetting(defaultSetting)
			}

			return user.createWithRegistrationRewardsTx(tx, inviterId, runtimeConfig)
		})
	}); err != nil {
		return err
	}

	user.finishInsert()
	return nil
}

func (user *User) finishInsert() {
	// 用户创建成功后，根据角色初始化边栏配置
	// 需要重新获取用户以确保有正确的ID和Role
	var createdUser User
	if err := DB.Where("username = ?", user.Username).First(&createdUser).Error; err == nil {
		// 生成基于角色的默认边栏配置
		defaultSidebarConfig := generateDefaultSidebarConfigForRole(createdUser.Role)
		if defaultSidebarConfig != "" {
			currentSetting := createdUser.GetSetting()
			currentSetting.SidebarModules = defaultSidebarConfig
			createdUser.SetSetting(currentSetting)
			if err := createdUser.Update(false); err != nil {
				common.SysLog(fmt.Sprintf("failed to initialize sidebar for new user %d: %v", createdUser.Id, err))
			} else {
				common.SysLog(fmt.Sprintf("为新用户 %s (角色: %d) 初始化边栏配置", createdUser.Username, createdUser.Role))
			}
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		common.SysLog(fmt.Sprintf("failed to reload new user %d for sidebar initialization: %v", user.Id, err))
	}

	if user.creationQuotaForNewUser > 0 {
		RecordLog(user.Id, LogTypeSystem, fmt.Sprintf("新用户注册赠送 %s", logger.LogQuota(user.creationQuotaForNewUser)))
	}
	if user.creationQuotaForInvitee > 0 {
		RecordLog(user.Id, LogTypeSystem, fmt.Sprintf("使用邀请码赠送 %s", logger.LogQuota(user.creationQuotaForInvitee)))
	}
	if user.creationQuotaForInviter > 0 && user.creationInviterId > 0 {
		RecordLog(user.creationInviterId, LogTypeSystem, fmt.Sprintf("邀请用户赠送 %s", logger.LogQuota(user.creationQuotaForInviter)))
	}
	user.creationQuotaForNewUser = 0
	user.creationQuotaForInvitee = 0
	user.creationQuotaForInviter = 0
	user.creationInviterId = 0
}

func (user *User) FinishInsert(_ int) {
	user.finishInsert()
}

// InsertWithTx inserts a new user within an existing transaction.
// This is used for OAuth registration where user creation and binding need to be atomic.
// Post-creation tasks (sidebar config, logs, inviter rewards) are handled after the transaction commits.
func (user *User) InsertWithTx(tx *gorm.DB, inviterId int) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}
	runtimeConfig := common.GetGeneralRuntimeConfig()
	return withNormalizedEmailLock(tx, user.Email, func(tx *gorm.DB) error {
		if err := user.prepareForInsert(tx); err != nil {
			return err
		}
		user.AffCode = common.GetRandomString(4)

		// 初始化用户设置
		if user.Setting == "" {
			defaultSetting := dto.UserSetting{}
			user.SetSetting(defaultSetting)
		}

		return user.createWithRegistrationRewardsTx(tx, inviterId, runtimeConfig)
	})
}

// FinalizeOAuthUserCreation performs post-transaction tasks for OAuth user creation.
// This should be called after the transaction commits successfully.
func (user *User) FinalizeOAuthUserCreation(_ int) {
	// 用户创建成功后，根据角色初始化边栏配置
	var createdUser User
	if err := DB.Where("id = ?", user.Id).First(&createdUser).Error; err == nil {
		defaultSidebarConfig := generateDefaultSidebarConfigForRole(createdUser.Role)
		if defaultSidebarConfig != "" {
			currentSetting := createdUser.GetSetting()
			currentSetting.SidebarModules = defaultSidebarConfig
			createdUser.SetSetting(currentSetting)
			if err := createdUser.Update(false); err != nil {
				common.SysLog(fmt.Sprintf("failed to initialize OAuth sidebar for user %d: %v", createdUser.Id, err))
			} else {
				common.SysLog(fmt.Sprintf("为新用户 %s (角色: %d) 初始化边栏配置", createdUser.Username, createdUser.Role))
			}
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		common.SysLog(fmt.Sprintf("failed to reload OAuth user %d for sidebar initialization: %v", user.Id, err))
	}

	if user.creationQuotaForNewUser > 0 {
		RecordLog(user.Id, LogTypeSystem, fmt.Sprintf("新用户注册赠送 %s", logger.LogQuota(user.creationQuotaForNewUser)))
	}
	if user.creationQuotaForInvitee > 0 {
		RecordLog(user.Id, LogTypeSystem, fmt.Sprintf("使用邀请码赠送 %s", logger.LogQuota(user.creationQuotaForInvitee)))
	}
	if user.creationQuotaForInviter > 0 && user.creationInviterId > 0 {
		RecordLog(user.creationInviterId, LogTypeSystem, fmt.Sprintf("邀请用户赠送 %s", logger.LogQuota(user.creationQuotaForInviter)))
	}
	user.creationQuotaForNewUser = 0
	user.creationQuotaForInvitee = 0
	user.creationQuotaForInviter = 0
	user.creationInviterId = 0
}

func (user *User) Update(updatePassword bool) error {
	var previousAuthVersion int64
	if err := DB.Model(&User{}).Where("id = ?", user.Id).Select("auth_version").Find(&previousAuthVersion).Error; err != nil {
		return err
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return user.UpdateWithTx(tx, updatePassword)
	}); err != nil {
		return err
	}
	if err := updateUserCache(*user); err != nil {
		return err
	}
	if user.AuthVersion > previousAuthVersion {
		_, err := RevokeAllUserSessions(user.Id, "user_security_changed")
		return err
	}
	return nil
}

func (user *User) UpdateWithTx(tx *gorm.DB, updatePassword bool) error {
	var err error
	if updatePassword {
		user.Password, err = common.Password2Hash(user.Password)
		if err != nil {
			return err
		}
	}
	newUser := *user
	current := User{}
	if err = tx.First(&current, user.Id).Error; err != nil {
		return err
	}
	// Updates(struct) ignores zero values. Match that behavior when deciding
	// whether this request actually changes authentication-sensitive state;
	// partial self-profile updates intentionally leave role/status/group empty.
	authChanged := (updatePassword && current.Password != newUser.Password) ||
		(newUser.Role != 0 && current.Role != newUser.Role) ||
		(newUser.Status != 0 && current.Status != newUser.Status) ||
		(newUser.Group != "" && current.Group != newUser.Group)
	if authChanged {
		newUser.AuthVersion, err = IncrementUserAuthVersionWithTx(tx, user.Id)
		if err != nil {
			return err
		}
	}
	if err = tx.Model(&current).Omit(
		"access_token",
		"access_token_ciphertext",
		"access_token_hash",
		"quota",
		"used_quota",
		"request_count",
		"aff_count",
		"aff_quota",
		"aff_history",
		"auth_version",
	).Updates(newUser).Error; err != nil {
		return err
	}
	return tx.First(user, user.Id).Error
}

func (user *User) Edit(updatePassword bool) error {
	var previousAuthVersion int64
	if err := DB.Model(&User{}).Where("id = ?", user.Id).Select("auth_version").Find(&previousAuthVersion).Error; err != nil {
		return err
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return user.EditWithTx(tx, updatePassword)
	}); err != nil {
		return err
	}
	if err := updateUserCache(*user); err != nil {
		return err
	}
	if user.AuthVersion > previousAuthVersion {
		_, err := RevokeAllUserSessions(user.Id, "user_security_changed")
		return err
	}
	return nil
}

func (user *User) EditWithTx(tx *gorm.DB, updatePassword bool) error {
	var err error
	if updatePassword {
		user.Password, err = common.Password2Hash(user.Password)
		if err != nil {
			return err
		}
	}

	newUser := *user
	updates := map[string]interface{}{
		"username":     newUser.Username,
		"display_name": newUser.DisplayName,
		"group":        newUser.Group,
		"remark":       newUser.Remark,
	}
	if updatePassword {
		updates["password"] = newUser.Password
	}

	current := User{}
	if err = tx.First(&current, user.Id).Error; err != nil {
		return err
	}
	authChanged := (updatePassword && current.Password != newUser.Password) || current.Group != newUser.Group
	if authChanged {
		newUser.AuthVersion, err = IncrementUserAuthVersionWithTx(tx, user.Id)
		if err != nil {
			return err
		}
	}
	if err = tx.Model(&current).Updates(updates).Error; err != nil {
		return err
	}
	return tx.First(user, user.Id).Error
}

func (user *User) ClearBinding(bindingType string) error {
	if user.Id == 0 {
		return errors.New("user id is empty")
	}

	bindingColumnMap := map[string]string{
		"email":    "email",
		"github":   "github_id",
		"discord":  "discord_id",
		"oidc":     "oidc_id",
		"wechat":   "wechat_id",
		"telegram": "telegram_id",
		"linuxdo":  "linux_do_id",
	}

	column, ok := bindingColumnMap[bindingType]
	if !ok {
		return errors.New("invalid binding type")
	}

	if err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", user.Id).Update(column, "").Error; err != nil {
			return err
		}
		if provider, ok := ExternalIdentityProviderForColumn(column); ok {
			return ReleaseExternalIdentityWithTx(tx, provider, user.Id)
		}
		return nil
	}); err != nil {
		return err
	}

	if err := DB.Where("id = ?", user.Id).First(user).Error; err != nil {
		return err
	}

	return updateUserCache(*user)
}

func (user *User) Delete() error {
	if user.Id == 0 {
		return errors.New("id 为空！")
	}
	var nextAuthVersion int64
	if err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		nextAuthVersion, err = IncrementUserAuthVersionWithTx(tx, user.Id)
		if err != nil {
			return err
		}
		return tx.Delete(user).Error
	}); err != nil {
		return err
	}
	if err := publishCommittedUserAuthVersion(user.Id, nextAuthVersion); err != nil {
		return err
	}
	if _, err := RevokeAllUserSessions(user.Id, "user_deleted"); err != nil {
		return err
	}
	return invalidateUserCache(user.Id)
}

func (user *User) HardDelete() error {
	if user.Id == 0 {
		return errors.New("id 为空！")
	}
	var tokens []Token
	var deletedAuthVersion int64
	var userCacheMutationID string
	tokenCacheMutationIDs := make(map[int]string)
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		deletedAuthVersion, err = IncrementUserAuthVersionWithTx(tx, user.Id)
		if err != nil {
			return err
		}
		if common.RedisEnabled {
			if err := tx.Unscoped().Select("id", mainKeyColumn(tx), "key_ciphertext", "key_hash").Where("user_id = ?", user.Id).Find(&tokens).Error; err != nil {
				return err
			}
			// Stage cache cleanup in this transaction before deleting the rows.
			// The token rows disappear on commit, so the repair worker must retain
			// each old hash key and treat a missing token as a successful tombstone.
			userCacheMutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, user.Id, getUserCacheKey(user.Id))
			if err != nil {
				return err
			}
			for _, token := range tokens {
				if token.Id <= 0 || strings.TrimSpace(token.Key) == "" {
					// Legacy rows may lack a usable cache identity. They are still
					// deleted atomically; there is simply no Redis key to repair.
					continue
				}
				mutationID, stageErr := stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityToken, token.Id, getTokenCacheKey(token.Key))
				if stageErr != nil {
					return stageErr
				}
				tokenCacheMutationIDs[token.Id] = mutationID
			}
		}
		if err := deleteUserAuthenticationData(tx, user.Id); err != nil {
			return err
		}
		return tx.Unscoped().Delete(user).Error
	})
	if err != nil {
		return err
	}
	if err := publishCommittedUserAuthVersion(user.Id, deletedAuthVersion); err != nil {
		common.SysError(fmt.Sprintf("failed to publish auth tombstone after hard deleting user %d: %v", user.Id, err))
	}
	for _, token := range tokens {
		if strings.TrimSpace(token.Key) == "" || !common.RedisEnabled {
			continue
		}
		cacheKey := getTokenCacheKey(token.Key)
		if err := invalidateTokenCacheForMutation(token.Key); err != nil {
			common.SysError(fmt.Sprintf("failed to invalidate token cache after hard deleting user %d token %d: %v", user.Id, token.Id, err))
			continue
		}
		if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityToken, token.Id, cacheKey, tokenCacheMutationIDs[token.Id]); err != nil {
			common.SysError(fmt.Sprintf("failed to complete token cache repair after hard deleting user %d token %d: %v", user.Id, token.Id, err))
		}
	}
	if err := invalidateUserCache(user.Id); err != nil {
		common.SysError(fmt.Sprintf("failed to invalidate user cache after hard deleting user %d: %v", user.Id, err))
	} else if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityUser, user.Id, getUserCacheKey(user.Id), userCacheMutationID); err != nil {
		common.SysError(fmt.Sprintf("failed to complete user cache repair after hard deleting user %d: %v", user.Id, err))
	}
	return nil
}

func deleteUserAuthenticationData(tx *gorm.DB, userId int) error {
	if err := releaseAllExternalIdentitiesWithTx(tx, userId); err != nil {
		return err
	}
	for _, authenticationData := range []any{
		&TwoFABackupCode{},
		&TwoFA{},
		&UserSession{},
		&AuthFlow{},
		&PasskeyCredential{},
		&Token{},
	} {
		if err := tx.Unscoped().Where("user_id = ?", userId).Delete(authenticationData).Error; err != nil {
			return err
		}
	}
	return deleteUserOAuthBindingsByUserId(tx, userId)
}

// ValidateAndFill check password & user status
func (user *User) ValidateAndFill() (err error) {
	// When querying with struct, GORM will only query with non-zero fields,
	// that means if your field's value is 0, '', false or other zero values,
	// it won't be used to build query conditions
	password := user.Password
	username := strings.TrimSpace(user.Username)
	if username == "" || password == "" {
		return ErrUserEmptyCredentials
	}
	// find by username or email
	err = DB.Where("username = ? OR email = ?", username, username).First(user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("%w: %v", ErrDatabase, err)
	}
	if user.Password == "" {
		return ErrInvalidCredentials
	}
	okay := common.ValidatePasswordAndHash(password, user.Password)
	if !okay || user.Status != common.UserStatusEnabled {
		return ErrInvalidCredentials
	}
	return nil
}

func (user *User) FillUserById() error {
	if user.Id == 0 {
		return errors.New("id 为空！")
	}
	return DB.Where(User{Id: user.Id}).First(user).Error
}

func (user *User) FillUserByEmail() error {
	if user.Email == "" {
		return errors.New("email 为空！")
	}
	return DB.Where(User{Email: NormalizeEmail(user.Email)}).First(user).Error
}

func (user *User) FillUserByGitHubId() error {
	if user.GitHubId == "" {
		return errors.New("GitHub id 为空！")
	}
	return DB.Where(User{GitHubId: user.GitHubId}).First(user).Error
}

// UpdateGitHubId updates the user's GitHub ID (used for migration from login to numeric ID)
func (user *User) UpdateGitHubId(newGitHubId string) error {
	newGitHubId = strings.TrimSpace(newGitHubId)
	if user.Id == 0 {
		return errors.New("user id is empty")
	}
	if newGitHubId == "" {
		return errors.New("github id is empty")
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ReleaseExternalIdentityWithTx(tx, ExternalIdentityProviderGitHub, user.Id); err != nil {
			return err
		}
		if err := ClaimExternalIdentityWithTx(tx, ExternalIdentityProviderGitHub, newGitHubId, user.Id); err != nil {
			return err
		}
		result := tx.Model(&User{}).Where("id = ?", user.Id).Update("github_id", newGitHubId)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&User{}).Where("id = ?", user.Id).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		return nil
	}); err != nil {
		return err
	}
	user.GitHubId = newGitHubId
	return nil
}

func (user *User) FillUserByDiscordId() error {
	if user.DiscordId == "" {
		return errors.New("discord id 为空！")
	}
	return DB.Where(User{DiscordId: user.DiscordId}).First(user).Error
}

func (user *User) FillUserByOidcId() error {
	if user.OidcId == "" {
		return errors.New("oidc id 为空！")
	}
	return DB.Where(User{OidcId: user.OidcId}).First(user).Error
}

func (user *User) FillUserByWeChatId() error {
	if user.WeChatId == "" {
		return errors.New("WeChat id 为空！")
	}
	return DB.Where(User{WeChatId: user.WeChatId}).First(user).Error
}

func (user *User) FillUserByTelegramId() error {
	if user.TelegramId == "" {
		return errors.New("Telegram id 为空！")
	}
	err := DB.Where(User{TelegramId: user.TelegramId}).First(user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("该 Telegram 账户未绑定")
	}
	return err
}

func IsEmailAlreadyTaken(email string) bool {
	count, err := CountUsersByEmail(email)
	return err == nil && count > 0
}

func GetUniqueUserByEmail(email string) (*User, error) {
	email = NormalizeEmail(email)
	if email == "" {
		return nil, ErrEmailNotFound
	}
	var users []User
	if err := DB.Where("LOWER(email) = ?", email).Limit(2).Find(&users).Error; err != nil {
		return nil, err
	}
	switch len(users) {
	case 0:
		return nil, ErrEmailNotFound
	case 1:
		return &users[0], nil
	default:
		return nil, ErrEmailAmbiguous
	}
}

// isExternalIdentityTaken fails closed when the database cannot answer. OAuth
// registration must never treat an outage as "identity is free", because that
// can race a uniqueness check and attach a provider identity to the wrong user.
func isExternalIdentityTaken(column, value string, includeDeleted bool) bool {
	if DB == nil {
		common.SysLog(fmt.Sprintf("failed to check external identity column=%s: database is not initialized", column))
		return true
	}
	query := DB
	if includeDeleted {
		query = query.Unscoped()
	}
	result := query.Where(column+" = ?", value).Limit(1).Find(&User{})
	if result.Error != nil {
		common.SysLog(fmt.Sprintf("failed to check external identity column=%s: %v", column, result.Error))
		return true
	}
	return result.RowsAffected > 0
}

func IsWeChatIdAlreadyTaken(wechatId string) bool {
	return isExternalIdentityTaken("wechat_id", wechatId, true)
}

func IsGitHubIdAlreadyTaken(githubId string) bool {
	return isExternalIdentityTaken("github_id", githubId, true)
}

func IsDiscordIdAlreadyTaken(discordId string) bool {
	return isExternalIdentityTaken("discord_id", discordId, true)
}

func IsOidcIdAlreadyTaken(oidcId string) bool {
	return isExternalIdentityTaken("oidc_id", oidcId, false)
}

func IsTelegramIdAlreadyTaken(telegramId string) bool {
	return isExternalIdentityTaken("telegram_id", telegramId, true)
}

func ResetUserPasswordByEmail(email string, password string) error {
	if email == "" || password == "" {
		return errors.New("邮箱地址或密码为空！")
	}
	user, err := GetUniqueUserByEmail(email)
	if err != nil {
		return err
	}
	hashedPassword, err := common.Password2Hash(password)
	if err != nil {
		return err
	}
	if err = DB.Transaction(func(tx *gorm.DB) error {
		if _, err := IncrementUserAuthVersionWithTx(tx, user.Id); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("password", hashedPassword).Error
	}); err != nil {
		return err
	}
	if err := PublishUserAuthCache(user.Id); err != nil {
		return err
	}
	_, err = RevokeAllUserSessions(user.Id, "password_reset")
	return err
}

func IsAdmin(userId int) bool {
	if userId == 0 {
		return false
	}
	var user User
	err := DB.Where("id = ?", userId).Select("role").Find(&user).Error
	if err != nil {
		common.SysLog("no such user " + err.Error())
		return false
	}
	return user.Role >= common.RoleAdminUser
}

func ValidateAccessToken(token string) (*User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, nil
	}
	return lookupUserByAccessToken(token)
}

// GetUserQuota gets quota from Redis first, falls back to DB if needed.  Batch
// mode intentionally bypasses the cache for this financial field: quota
// reservations are DB-conditional there, and a process can exit before the
// post-commit cache fence is written.  Serving a cached value in that window
// would let trust/pre-consume decisions observe a stale balance.
func GetUserQuota(id int, fromDB bool) (quota int, err error) {
	if id <= 0 {
		return 0, errors.New("user id is invalid")
	}
	if !fromDB && common.RedisEnabled && !common.BatchUpdateEnabled {
		return getUserQuotaCache(id)
	}
	result := DB.Model(&User{}).Where("id = ?", id).Select("quota").Find(&quota)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, gorm.ErrRecordNotFound
	}

	return quota, nil
}

func GetUserUsedQuota(id int) (quota int, err error) {
	if id <= 0 {
		return 0, errors.New("user id is invalid")
	}
	result := DB.Model(&User{}).Where("id = ?", id).Select("used_quota").Find(&quota)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	return quota, nil
}

func GetUserEmail(id int) (email string, err error) {
	if id <= 0 {
		return "", errors.New("user id is invalid")
	}
	result := DB.Model(&User{}).Where("id = ?", id).Select("email").Find(&email)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", gorm.ErrRecordNotFound
	}
	return email, nil
}

// GetUserGroup gets group from Redis first, falls back to DB if needed
func GetUserGroup(id int, fromDB bool) (group string, err error) {
	if id <= 0 {
		return "", errors.New("user id is invalid")
	}
	defer func() {
		// Update Redis cache asynchronously on successful DB read
		if shouldUpdateRedis(fromDB, err) {
			gopool.Go(func() {
				if err := RefreshUserGroupCache(id); err != nil {
					common.SysLog("failed to update user group cache: " + err.Error())
				}
			})
		}
	}()
	if !fromDB && common.RedisEnabled {
		group, err := getUserGroupCache(id)
		if err == nil {
			return group, nil
		}
		// Don't return error - fall through to DB
	}
	fromDB = true
	result := DB.Model(&User{}).Where("id = ?", id).Select(commonGroupCol).Find(&group)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", gorm.ErrRecordNotFound
	}

	return group, nil
}

// GetUserSetting gets setting from Redis first, falls back to DB if needed
func GetUserSetting(id int, fromDB bool) (settingMap dto.UserSetting, err error) {
	if id <= 0 {
		return settingMap, errors.New("user id is invalid")
	}
	var setting string
	defer func() {
		// Update Redis cache asynchronously on successful DB read
		if shouldUpdateRedis(fromDB, err) {
			gopool.Go(func() {
				if err := updateUserSettingCache(id, setting); err != nil {
					common.SysLog("failed to update user setting cache: " + err.Error())
				}
			})
		}
	}()
	if !fromDB && common.RedisEnabled {
		setting, err := getUserSettingCache(id)
		if err == nil {
			return setting, nil
		}
		// Don't return error - fall through to DB
	}
	fromDB = true
	// can be nil setting
	var safeSetting sql.NullString
	result := DB.Model(&User{}).Where("id = ?", id).Select("setting").Find(&safeSetting)
	if result.Error != nil {
		return settingMap, result.Error
	}
	if result.RowsAffected == 0 {
		return settingMap, gorm.ErrRecordNotFound
	}
	if safeSetting.Valid {
		setting = safeSetting.String
	} else {
		setting = ""
	}
	userBase := &UserBase{
		Setting: setting,
	}
	return userBase.GetSettingWithError()
}

// SetUserQuota writes an absolute wallet balance to the database and fences
// the corresponding Redis snapshot after the commit.  Wallet quota is
// financial state, so this operation deliberately bypasses the batch update
// queue.  A cache invalidation failure is non-fatal once the database write
// has succeeded: the shared mutation-failure handler drops the key when
// possible and records a durable repair hint when Redis is unavailable.
func SetUserQuota(id int, quota int) error {
	if id <= 0 {
		return errors.New("user id is invalid")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return err
	}

	var mutationID string
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).Where("id = ?", id).Update("quota", quota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// MySQL may report zero changed rows when the requested absolute value
			// already equals the stored value. Distinguish that legitimate no-op
			// from an unknown user before returning.
			var count int64
			if err := tx.Model(&User{}).Where("id = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		var err error
		mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, id, getUserCacheKey(id))
		return err
	})
	if err != nil {
		return err
	}

	cacheSafe := true
	if err := invalidateUserCache(id); err != nil {
		cacheSafe = false
		common.SysLog("failed to invalidate user quota cache: " + err.Error())
	}
	if cacheSafe {
		if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), mutationID); err != nil {
			common.SysLog("failed to complete user quota cache repair: " + err.Error())
		}
	}
	return nil
}

func IncreaseUserQuota(id int, quota int, db bool) (err error) {
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return err
	}
	// A zero delta is an intentional no-op. Do not turn it into an existence
	// check: callers use zero while unwinding optional billing components, and
	// a missing row must not be reported as a failed zero-value mutation.
	if quota == 0 {
		return nil
	}
	// Wallet quota is a spendable financial ledger.  Never enqueue it in the
	// process-local batch maps: a crash before the next batch flush would lose
	// an already accepted credit and leave Redis/DB permanently divergent.
	// The `db` argument is retained for API compatibility; both branches now
	// use the same durable write while only informational aggregates remain
	// batchable.
	mutationID, err := increaseUserQuota(id, quota)
	if err != nil {
		return err
	}
	cacheSafe := true
	if err := cacheIncrUserQuota(id, int64(quota)); err != nil {
		common.SysLog("failed to increase user quota: " + err.Error())
		if repairErr := repairUserQuotaCache(id); repairErr != nil {
			cacheSafe = false
			recordQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), repairErr)
		}
	}
	if cacheSafe {
		if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), mutationID); err != nil {
			common.SysLog("failed to complete user quota cache repair: " + err.Error())
		}
	}
	return nil
}

func increaseUserQuota(id int, quota int) (mutationID string, err error) {
	err = DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).
			Where("id = ? AND quota <= ?", id, common.MaxWalletQuota-quota).
			Update("quota", gorm.Expr("quota + ?", quota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var count int64
			if err := tx.Model(&User{}).Where("id = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
			return ErrWalletQuotaLimitExceeded
		}
		mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, id, getUserCacheKey(id))
		return err
	})
	return mutationID, err
}

func DecreaseUserQuota(id int, quota int, db bool) (err error) {
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	// A zero delta is an intentional no-op (including for a stale/deleted
	// account); avoid an unnecessary cache/database write and false not-found
	// result from the affected-row guard below.
	if quota == 0 {
		return nil
	}
	// Wallet quota is financial state and therefore always reaches the
	// database before this function returns, even when batch statistics are
	// enabled.  Keeping the cache update after a successful DB write preserves
	// the existing cache fencing/repair semantics.
	mutationID, err := decreaseUserQuota(id, quota)
	if err != nil {
		return err
	}
	// Update the cache only after the database write succeeds. Otherwise a
	// deleted/missing user could have its cached balance decremented even though
	// the corresponding ledger write failed.
	cacheSafe := true
	if err := cacheDecrUserQuota(id, int64(quota)); err != nil {
		common.SysLog("failed to decrease user quota: " + err.Error())
		if repairErr := repairUserQuotaCache(id); repairErr != nil {
			cacheSafe = false
			recordQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), repairErr)
		}
	}
	if cacheSafe {
		if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), mutationID); err != nil {
			common.SysLog("failed to complete user quota cache repair: " + err.Error())
		}
	}
	return nil
}

func decreaseUserQuota(id int, quota int) (mutationID string, err error) {
	if quota == 0 {
		return "", nil
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).Where("id = ?", id).Update("quota", gorm.Expr("quota - ?", quota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Some MySQL configurations report zero changed rows for a no-op
			// update. Distinguish that from an unknown user before staging repair.
			var count int64
			if err := tx.Model(&User{}).Where("id = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, id, getUserCacheKey(id))
		return err
	})
	return mutationID, err
}

func DeltaUpdateUserQuota(id int, delta int) (err error) {
	if delta == 0 {
		return nil
	}
	if delta > 0 {
		return IncreaseUserQuota(id, delta, false)
	} else {
		return DecreaseUserQuota(id, -delta, false)
	}
}

//func GetRootUserEmail() (email string) {
//	DB.Model(&User{}).Where("role = ?", common.RoleRootUser).Select("email").Find(&email)
//	return email
//}

// GetRootUserWithError preserves database failures for notification and
// bootstrap callers. A missing root is distinct from an unavailable database.
func GetRootUserWithError() (*User, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var user User
	err := DB.Where("role = ?", common.RoleRootUser).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetRootUser is retained for compatibility with older internal callers. It
// logs rather than silently returning a nil user when the query fails.
func GetRootUser() (user *User) {
	user, err := GetRootUserWithError()
	if err != nil {
		common.SysLog("failed to load root user: " + err.Error())
		return nil
	}
	return user
}

func UpdateUserLastLoginAt(id int) {
	if err := DB.Model(&User{}).Where("id = ?", id).Update("last_login_at", common.GetTimestamp()).Error; err != nil {
		common.SysLog("failed to update user last_login_at: " + err.Error())
	}
}

func UpdateUserUsedQuotaAndRequestCount(id int, quota int) {
	if common.BatchUpdateEnabled {
		addNewRecord(BatchUpdateTypeUsedQuota, id, quota)
		addNewRecord(BatchUpdateTypeRequestCount, id, 1)
		return
	}
	updateUserUsedQuotaAndRequestCount(id, quota, 1)
}

// UpdateUserUsedQuotaAndRequestCountE is the error-returning counterpart used
// by durable billing compatibility paths.  The historical helper intentionally
// logged and ignored database errors because it was called from telemetry
// paths; billing recovery must instead keep the operation retryable when the
// row update does not commit.
func UpdateUserUsedQuotaAndRequestCountE(id int, quota int) error {
	if id <= 0 {
		return errors.New("user id is invalid")
	}
	if quota < -common.MaxQuota || quota > common.MaxQuota {
		return fmt.Errorf("used quota delta is out of range: %d", quota)
	}
	if quota == 0 {
		// Request-count accounting is still a meaningful event for a zero-cost
		// request, so do not short-circuit the update altogether.
	}
	result := DB.Model(&User{}).Where("id = ?", id).Updates(map[string]interface{}{
		"used_quota":    gorm.Expr("used_quota + ?", quota),
		"request_count": gorm.Expr("request_count + ?", 1),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := DB.Model(&User{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	// MySQL may report zero changed rows for a same-value update; the row's
	// existence is sufficient because request_count is incremented atomically.
	return nil
}

// UpdateUserUsedQuota adjusts accumulated usage without changing request count.
func UpdateUserUsedQuota(id int, quota int) {
	if common.BatchUpdateEnabled {
		addNewRecord(BatchUpdateTypeUsedQuota, id, quota)
		return
	}
	if err := DB.Model(&User{}).Where("id = ?", id).Update("used_quota", gorm.Expr("used_quota + ?", quota)).Error; err != nil {
		common.SysLog("failed to update user used quota: " + err.Error())
	}
}

func updateUserUsedQuotaAndRequestCount(id int, quota int, count int) {
	err := DB.Model(&User{}).Where("id = ?", id).Updates(
		map[string]interface{}{
			"used_quota":    gorm.Expr("used_quota + ?", quota),
			"request_count": gorm.Expr("request_count + ?", count),
		},
	).Error
	if err != nil {
		common.SysLog("failed to update user used quota and request count: " + err.Error())
		return
	}

	//// 更新缓存
	//if err := invalidateUserCache(id); err != nil {
	//	common.SysError("failed to invalidate user cache: " + err.Error())
	//}
}

func updateUserQuotaUsedQuotaAndRequestCount(id int, quota int, usedQuota int, requestCount int) error {
	if quota == 0 && usedQuota == 0 && requestCount == 0 {
		return nil
	}

	result := DB.Model(&User{}).Where("id = ?", id).Updates(
		map[string]interface{}{
			"quota":         gorm.Expr("quota + ?", quota),
			"used_quota":    gorm.Expr("used_quota + ?", usedQuota),
			"request_count": gorm.Expr("request_count + ?", requestCount),
		},
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetUsernameById gets username from Redis first, falls back to DB if needed
func GetUsernameById(id int, fromDB bool) (username string, err error) {
	defer func() {
		// Update Redis cache asynchronously on successful DB read
		if shouldUpdateRedis(fromDB, err) {
			gopool.Go(func() {
				if err := updateUserNameCache(id, username); err != nil {
					common.SysLog("failed to update user name cache: " + err.Error())
				}
			})
		}
	}()
	if !fromDB && common.RedisEnabled {
		username, err := getUserNameCache(id)
		if err == nil {
			return username, nil
		}
		// Don't return error - fall through to DB
	}
	fromDB = true
	err = DB.Model(&User{}).Where("id = ?", id).Select("username").Find(&username).Error
	if err != nil {
		return "", err
	}

	return username, nil
}

func IsLinuxDOIdAlreadyTaken(linuxDOId string) bool {
	var user User
	err := DB.Unscoped().Where("linux_do_id = ?", linuxDOId).First(&user).Error
	return !errors.Is(err, gorm.ErrRecordNotFound)
}

func (user *User) FillUserByLinuxDOId() error {
	if user.LinuxDOId == "" {
		return errors.New("linux do id is empty")
	}
	err := DB.Where("linux_do_id = ?", user.LinuxDOId).First(user).Error
	return err
}

func RootUserExists() bool {
	exists, err := RootUserExistsWithError()
	return err == nil && exists
}

// RootUserExistsWithError is the fail-closed counterpart used by bootstrap
// handlers.  The historical bool-only helper intentionally keeps its API for
// callers that only need a hint, but treating a database outage as "no root"
// would make an unhealthy instance advertise a first-run setup flow.
func RootUserExistsWithError() (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var user User
	err := DB.Where("role = ?", common.RoleRootUser).Select("id").First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
