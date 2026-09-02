package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

type Token struct {
	Id     int `json:"id"`
	UserId int `json:"user_id" gorm:"index"`
	// Key is a runtime-only plaintext credential.  The physical `key` column is
	// retained as LegacyKey for backwards-compatible schema/unique-index
	// handling, but new writes store only a non-reversible fingerprint marker
	// there; the reversible value lives in KeyCiphertext.
	Key           string  `json:"key" gorm:"-"`
	LegacyKey     *string `json:"-" gorm:"column:key;type:varchar(128);uniqueIndex"`
	KeyCiphertext string  `json:"-" gorm:"column:key_ciphertext;type:text"`
	// Keep this index non-unique.  The historical `key` column stores the
	// deterministic fingerprint marker and retains the uniqueness fence; a
	// separate unique constraint on a newly-added nullable column is not
	// portable (SQLite rejects ALTER TABLE ... ADD COLUMN ... UNIQUE).
	KeyHash            *string        `json:"-" gorm:"column:key_hash;type:char(64);index"`
	Status             int            `json:"status" gorm:"default:1"`
	Name               string         `json:"name" gorm:"index" `
	CreatedTime        int64          `json:"created_time" gorm:"bigint"`
	AccessedTime       int64          `json:"accessed_time" gorm:"bigint"`
	ExpiredTime        int64          `json:"expired_time" gorm:"bigint;default:-1"` // -1 means never expired
	RemainQuota        int            `json:"remain_quota" gorm:"default:0"`
	UnlimitedQuota     bool           `json:"unlimited_quota"`
	ModelLimitsEnabled bool           `json:"model_limits_enabled"`
	ModelLimits        string         `json:"model_limits" gorm:"type:text"`
	AllowIps           *string        `json:"allow_ips" gorm:"default:''"`
	UsedQuota          int            `json:"used_quota" gorm:"default:0"` // used quota
	Group              string         `json:"group" gorm:"default:''"`
	CrossGroupRetry    bool           `json:"cross_group_retry"` // 跨分组重试，仅auto分组有效
	AutoGroups         string         `json:"-" gorm:"type:text"`
	DeletedAt          gorm.DeletedAt `gorm:"index"`
}

func (token *Token) GetAutoGroups() ([]string, error) {
	if token.AutoGroups == "" {
		return nil, nil
	}
	var groups []string
	if err := common.UnmarshalJsonStr(token.AutoGroups, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (token *Token) SetAutoGroups(groups []string) error {
	if len(groups) == 0 {
		token.AutoGroups = ""
		return nil
	}
	data, err := common.Marshal(groups)
	if err != nil {
		return err
	}
	token.AutoGroups = string(data)
	return nil
}

func (token *Token) Clean() {
	token.Key = ""
}

func MaskTokenKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return strings.Repeat("*", len(key))
	}
	if len(key) <= 8 {
		return key[:2] + "****" + key[len(key)-2:]
	}
	return key[:4] + "**********" + key[len(key)-4:]
}

func (token *Token) GetFullKey() string {
	return token.Key
}

func (token *Token) GetMaskedKey() string {
	return MaskTokenKey(token.Key)
}

func (token *Token) GetIpLimits() []string {
	// delete empty spaces
	//split with \n
	ipLimits := make([]string, 0)
	if token.AllowIps == nil {
		return ipLimits
	}
	cleanIps := strings.ReplaceAll(*token.AllowIps, " ", "")
	if cleanIps == "" {
		return ipLimits
	}
	ips := strings.Split(cleanIps, "\n")
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		ip = strings.ReplaceAll(ip, ",", "")
		if ip != "" {
			ipLimits = append(ipLimits, ip)
		}
	}
	return ipLimits
}

func GetAllUserTokens(userId int, startIdx int, num int) ([]*Token, error) {
	var tokens []*Token
	var err error
	err = DB.Where("user_id = ?", userId).Order("id desc").Limit(num).Offset(startIdx).Find(&tokens).Error
	return tokens, err
}

// sanitizeLikePattern 校验并清洗用户输入的 LIKE 搜索模式。
// 规则：
//  1. 转义 ! 和 _（使用 ! 作为 ESCAPE 字符，兼容 MySQL/PostgreSQL/SQLite）
//  2. 连续的 % 合并为单个 %
//  3. 最多允许 2 个 %
//  4. 含 % 时（模糊搜索），去掉 % 后关键词长度必须 >= 2
//  5. 不含 % 时按精确匹配
func sanitizeLikePattern(input string) (string, error) {
	// 1. 先转义 ESCAPE 字符 ! 自身，再转义 _
	//    使用 ! 而非 \ 作为 ESCAPE 字符，避免 MySQL 中反斜杠的字符串转义问题
	input = strings.ReplaceAll(input, "!", "!!")
	input = strings.ReplaceAll(input, `_`, `!_`)

	if err := validateLikePattern(input); err != nil {
		return "", err
	}

	// 5. 无 % 时，精确全匹配
	return input, nil
}

func validateLikePattern(input string) error {
	// 1. 连续的 % 直接拒绝
	if strings.Contains(input, "%%") {
		return errors.New("搜索模式中不允许包含连续的 % 通配符")
	}

	// 2. 统计 % 数量，不得超过 2
	count := strings.Count(input, "%")
	if count > 2 {
		return errors.New("搜索模式中最多允许包含 2 个 % 通配符")
	}

	// 3. 含 % 时，去掉 % 后关键词长度必须 >= 2
	if count > 0 {
		stripped := strings.ReplaceAll(input, "%", "")
		if len(stripped) < 2 {
			return errors.New("使用模糊搜索时，关键词长度至少为 2 个字符")
		}
	}

	return nil
}

const searchHardLimit = 100

// searchTokenCountHardLimit bounds the total probe used by token search.  It
// is independent from searchHardLimit (the page size) so a malicious search
// cannot force an unbounded aggregate COUNT.
const searchTokenCountHardLimit = 10000

func SearchUserTokens(userId int, keyword string, token string, offset int, limit int) (tokens []*Token, total int64, err error) {
	// model 层强制截断
	if limit <= 0 || limit > searchHardLimit {
		limit = searchHardLimit
	}
	if offset < 0 {
		offset = 0
	}

	if token != "" {
		token = strings.TrimPrefix(token, "sk-")
	}

	// 超量用户（令牌数超过上限）只允许精确搜索，禁止模糊搜索
	maxTokens := operation_setting.GetMaxUserTokens()
	hasFuzzy := strings.Contains(keyword, "%") || strings.Contains(token, "%")
	if hasFuzzy {
		count, err := CountUserTokens(userId)
		if err != nil {
			common.SysLog("failed to count user tokens: " + err.Error())
			return nil, 0, errors.New("获取令牌数量失败")
		}
		if int(count) > maxTokens {
			return nil, 0, errors.New("令牌数量超过上限，仅允许精确搜索，请勿使用 % 通配符")
		}
	}

	baseQuery := DB.Model(&Token{}).Where("user_id = ?", userId)

	// 非空才加 LIKE 条件，空则跳过（不过滤该字段）
	if keyword != "" {
		keywordPattern, err := sanitizeLikePattern(keyword)
		if err != nil {
			return nil, 0, err
		}
		baseQuery = baseQuery.Where("name LIKE ? ESCAPE '!'", keywordPattern)
	}
	if token != "" {
		// New rows are indexed by a keyed fingerprint; retain the historical
		// LIKE path for legacy plaintext rows during rolling migration. Fuzzy
		// searches cannot be answered from a one-way hash, so they naturally
		// return only rows that have not migrated yet.
		if !strings.Contains(token, "%") {
			fingerprint := common.CredentialFingerprint(token)
			baseQuery = baseQuery.Where("key_hash = ? OR "+mainKeyColumn(DB)+" = ?", fingerprint, token)
		} else {
			tokenPattern, err := sanitizeLikePattern(token)
			if err != nil {
				return nil, 0, err
			}
			baseQuery = baseQuery.Where(mainKeyColumn(DB)+" LIKE ? ESCAPE '!'", tokenPattern)
		}
	}

	// 先查匹配总数（用于分页）。A LIMIT on Count is ignored by many SQL
	// optimizers because the aggregate is evaluated first, so read only a
	// bounded number of primary keys instead.
	countLimit := maxTokens
	if countLimit <= 0 || countLimit > searchTokenCountHardLimit {
		countLimit = searchTokenCountHardLimit
	}
	total, err = boundedPrimaryKeyCount(baseQuery, countLimit)
	if err != nil {
		common.SysError("failed to count search tokens: " + err.Error())
		return nil, 0, errors.New("搜索令牌失败")
	}

	// 再分页查数据
	err = baseQuery.Order("id desc").Offset(offset).Limit(limit).Find(&tokens).Error
	if err != nil {
		common.SysError("failed to search tokens: " + err.Error())
		return nil, 0, errors.New("搜索令牌失败")
	}
	return tokens, total, nil
}

func ValidateUserToken(key string) (token *Token, err error) {
	if key == "" {
		return nil, ErrTokenNotProvided
	}
	token, err = GetTokenByKey(key, false)
	if err == nil {
		if token.Status == common.TokenStatusExhausted ||
			token.Status == common.TokenStatusExpired ||
			token.Status != common.TokenStatusEnabled {
			return token, ErrTokenInvalid
		}
		if token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp() {
			if !common.RedisEnabled {
				token.Status = common.TokenStatusExpired
				err := token.SelectUpdate()
				if err != nil {
					common.SysLog("failed to update token status" + err.Error())
				}
			}
			return token, ErrTokenInvalid
		}
		if !token.UnlimitedQuota && token.RemainQuota <= 0 {
			if !common.RedisEnabled {
				token.Status = common.TokenStatusExhausted
				err := token.SelectUpdate()
				if err != nil {
					common.SysLog("failed to update token status" + err.Error())
				}
			}
			return token, ErrTokenInvalid
		}
		return token, nil
	}
	common.SysLog("ValidateUserToken: failed to get token: " + err.Error())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTokenInvalid
	}
	return nil, fmt.Errorf("%w: %v", ErrDatabase, err)
}

func GetTokenByIds(id int, userId int) (*Token, error) {
	if id <= 0 || userId <= 0 {
		return nil, errors.New("id 或 userId 无效！")
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	token := Token{Id: id, UserId: userId}
	if err := DB.First(&token, "id = ? and user_id = ?", id, userId).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func GetTokenById(id int) (*Token, error) {
	if id <= 0 {
		return nil, errors.New("id 无效！")
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	token := Token{Id: id}
	if err := DB.First(&token, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

// tokenKeyByIDTx resolves the cache identity from the same transaction that
// mutates quota. Callers may hold a stale key after a long-running task, while
// the database ID remains the authoritative ledger identity.
func tokenKeyByIDTx(tx *gorm.DB, id int) (string, error) {
	if tx == nil || id <= 0 {
		return "", errors.New("invalid token cache identity")
	}
	var token Token
	result := tx.Select("id", mainKeyColumn(tx), "key_ciphertext", "key_hash").Where("id = ?", id).Limit(1).First(&token)
	if result.Error != nil {
		return "", result.Error
	}
	key := token.Key
	if strings.TrimSpace(key) == "" {
		return "", errors.New("token key is empty")
	}
	return key, nil
}

func GetTokenByKey(key string, fromDB bool) (token *Token, err error) {
	if key == "" {
		return nil, errors.New("token key is empty")
	}
	// Batch mode keeps spendable token quota in the database (rather than the
	// process-local batch map).  Skip the Redis snapshot on reads so a process
	// restart or a cache-fence race cannot feed a stale RemainQuota into the
	// trust/pre-consume checks.  Metadata remains cached in the normal mode.
	if !fromDB && common.RedisEnabled && !common.BatchUpdateEnabled {
		// Try Redis first
		token, err := cacheGetTokenByKey(key)
		if err == nil {
			return token, nil
		}
		// Don't return error - fall through to DB
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	// Fingerprint lookup is the fast/secret-safe path for migrated rows. The
	// legacy key-column fallback only keeps an unmigrated row readable while
	// the new binary performs the one-time maintenance migration; old binaries
	// must not run against a database after marker conversion starts.
	fingerprint := common.CredentialFingerprint(key)
	token = &Token{}
	result := DB.Where("key_hash = ?", fingerprint).First(token)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		token = &Token{}
		result = DB.Where(mainKeyColumn(DB)+" = ?", key).First(token)
	}
	if result.Error != nil {
		return nil, result.Error
	}
	if token.Key != key {
		// A hash hit must still be verified against the decrypted value. This is
		// defensive against accidental hash-key rotation or an astronomically
		// unlikely collision; never authenticate a mismatched credential.
		return nil, gorm.ErrRecordNotFound
	}
	if common.RedisEnabled && !common.BatchUpdateEnabled {
		// 冷缓存时用数据库快照初始化；已存在的哈希只刷新 TTL，
		// 避免快照覆盖 Redis 中已被原子预扣的余额。初始化失败不影响本次读取。
		if _, cacheErr := cacheInitToken(*token); cacheErr != nil {
			common.SysLog("failed to init token cache: " + cacheErr.Error())
		}
	}
	return token, nil
}

func (token *Token) Insert() error {
	var err error
	err = DB.Create(token).Error
	return err
}

// tokenCacheKeyForMutation resolves the current cache identity before a
// mutation starts. Callers often hold a token struct loaded earlier, and the
// key may have been rotated since then; using the database value for the
// pre-write fence prevents that stale struct from fencing only an obsolete
// Redis hash. If the lookup itself fails, retain the caller's key and let the
// authoritative database mutation decide whether it can proceed.
func tokenCacheKeyForMutation(id int, fallback string) string {
	key := strings.TrimSpace(fallback)
	if id <= 0 || DB == nil {
		return key
	}
	var current Token
	result := DB.Unscoped().Select("id", mainKeyColumn(DB), "key_ciphertext", "key_hash").Where("id = ?", id).First(&current)
	if result.Error == nil && strings.TrimSpace(current.Key) != "" {
		return strings.TrimSpace(current.Key)
	}
	return key
}

// tokenCacheRepairIdentityTx resolves the key on the same transaction that
// performs a token mutation. The transaction's value is authoritative for the
// durable repair marker, while fallback keeps delete paths repairable even
// when the row has already disappeared (for example after a hard delete).
func tokenCacheRepairIdentityTx(tx *gorm.DB, id int, fallback string) (string, error) {
	key := strings.TrimSpace(fallback)
	if tx == nil || id <= 0 {
		return key, nil
	}
	var current Token
	result := tx.Unscoped().Select("id", mainKeyColumn(tx), "key_ciphertext", "key_hash").Where("id = ?", id).First(&current)
	if result.Error == nil {
		if strings.TrimSpace(current.Key) != "" {
			key = strings.TrimSpace(current.Key)
		}
		return key, nil
	}
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return key, nil
	}
	return "", result.Error
}

func stageTokenCacheRepairForMutationTx(tx *gorm.DB, id int, fallback string) (mutationID, key string, err error) {
	key, err = tokenCacheRepairIdentityTx(tx, id, fallback)
	if err != nil {
		return "", key, err
	}
	if common.RedisEnabled && id > 0 && key != "" {
		mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityToken, id, getTokenCacheKey(key))
	}
	return mutationID, key, err
}

// finishTokenCacheMutation performs the post-commit fence. A failure is
// deliberately non-fatal after the database commit: the staged repair row is
// retained and the background worker retries it until Redis converges.
func finishTokenCacheMutation(id int, key, mutationID string) {
	if !common.RedisEnabled || id <= 0 || strings.TrimSpace(key) == "" {
		return
	}
	cacheKey := getTokenCacheKey(key)
	if cacheErr := invalidateTokenCacheForMutation(key); cacheErr != nil {
		common.SysLog("failed to invalidate token cache after mutation: " + cacheErr.Error())
		return
	}
	if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityToken, id, cacheKey, mutationID); err != nil {
		common.SysLog("failed to complete token cache repair after mutation: " + err.Error())
	}
}

// Update Make sure your token's fields is completed, because this will update non-zero values
func (token *Token) Update() (err error) {
	// 写库前失效缓存并设置 fence，防止并发读者把过期快照重新写回缓存。
	if cacheErr := invalidateTokenCacheForMutation(token.Key); cacheErr != nil {
		common.SysLog("failed to invalidate token cache before update: " + cacheErr.Error())
	}
	var mutationID string
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(token).Select("name", "status", "expired_time", "remain_quota", "unlimited_quota",
			"model_limits_enabled", "model_limits", "allow_ips", "group", "cross_group_retry", "auto_groups").Updates(token).Error; err != nil {
			return err
		}
		mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityToken, token.Id, getTokenCacheKey(token.Key))
		return err
	})
	if err != nil {
		return err
	}
	if cacheErr := invalidateTokenCacheForMutation(token.Key); cacheErr != nil {
		common.SysLog("failed to invalidate token cache after update: " + cacheErr.Error())
		return nil
	}
	if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityToken, token.Id, getTokenCacheKey(token.Key), mutationID); err != nil {
		common.SysLog("failed to complete token update cache repair: " + err.Error())
	}
	return nil
}

func (token *Token) SelectUpdate() (err error) {
	if token == nil {
		return errors.New("token is nil")
	}
	authoritativeKey := tokenCacheKeyForMutation(token.Id, token.Key)
	if cacheErr := invalidateTokenCacheForMutation(authoritativeKey); cacheErr != nil {
		common.SysLog("failed to invalidate token cache before status update: " + cacheErr.Error())
	}
	var mutationID string
	err = DB.Transaction(func(tx *gorm.DB) error {
		var err error
		mutationID, authoritativeKey, err = stageTokenCacheRepairForMutationTx(tx, token.Id, authoritativeKey)
		if err != nil {
			return err
		}
		// This can update zero values.
		if err := tx.Model(token).Select("accessed_time", "status").Updates(token).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	finishTokenCacheMutation(token.Id, authoritativeKey, mutationID)
	return nil
}

func (token *Token) Delete() (err error) {
	if token == nil {
		return errors.New("token is nil")
	}
	authoritativeKey := tokenCacheKeyForMutation(token.Id, token.Key)
	if cacheErr := invalidateTokenCacheForMutation(authoritativeKey); cacheErr != nil {
		common.SysLog("failed to invalidate token cache before delete: " + cacheErr.Error())
	}
	var mutationID string
	err = DB.Transaction(func(tx *gorm.DB) error {
		var err error
		authoritativeKey, err = tokenCacheRepairIdentityTx(tx, token.Id, authoritativeKey)
		if err != nil {
			return err
		}
		if err := tx.Delete(token).Error; err != nil {
			return err
		}
		if common.RedisEnabled && token.Id > 0 && authoritativeKey != "" {
			mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityToken, token.Id, getTokenCacheKey(authoritativeKey))
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	finishTokenCacheMutation(token.Id, authoritativeKey, mutationID)
	return nil
}

func (token *Token) IsModelLimitsEnabled() bool {
	return token.ModelLimitsEnabled
}

func (token *Token) GetModelLimits() []string {
	if token.ModelLimits == "" {
		return []string{}
	}
	return strings.Split(token.ModelLimits, ",")
}

func (token *Token) GetModelLimitsMap() map[string]bool {
	limits := token.GetModelLimits()
	limitsMap := make(map[string]bool)
	for _, limit := range limits {
		limitsMap[limit] = true
	}
	return limitsMap
}

func DisableModelLimits(tokenId int) error {
	token, err := GetTokenById(tokenId)
	if err != nil {
		return err
	}
	token.ModelLimitsEnabled = false
	token.ModelLimits = ""
	return token.Update()
}

func DeleteTokenById(id int, userId int) (err error) {
	// Why we need userId here? In case user want to delete other's token.
	if id == 0 || userId == 0 {
		return errors.New("id 或 userId 为空！")
	}
	token := Token{Id: id, UserId: userId}
	err = DB.Where(token).First(&token).Error
	if err != nil {
		return err
	}
	return token.Delete()
}

func IncreaseTokenQuota(tokenId int, key string, quota int) (err error) {
	if quota < 0 || quota > common.MaxQuota {
		return fmt.Errorf("quota 超出范围: %d", quota)
	}
	// A zero delta is an intentional no-op. In particular, do not report a
	// deleted token as missing when a caller is unwinding an optional zero
	// reservation.
	if quota == 0 {
		return nil
	}
	// Token quota is also a spendable financial ledger.  It must not wait in
	// the process-local batch queue: a crash before the next flush would lose
	// a refund (or a compensating reservation) and leave Redis ahead of the DB.
	// BatchUpdateEnabled remains reserved for informational usage counters.
	mutationID, authoritativeKey, err := increaseTokenQuotaWithCacheRepair(tokenId, quota)
	if err != nil {
		return err
	}
	cacheSafe := true
	if common.RedisEnabled {
		// 守卫式增量：哈希不存在时跳过，由下次读取从数据库水合，
		// 绝不创建只有配额字段的残缺哈希。
		result, cacheErr := cacheApplyTokenQuotaDelta(tokenId, authoritativeKey, int64(quota))
		if cacheErr != nil || result != cacheQuotaOK {
			if cacheErr == nil {
				cacheErr = ErrQuotaCacheMiss
			}
			common.SysLog("failed to increase token quota cache: " + cacheErr.Error())
			if repairErr := invalidateTokenCacheForMutation(authoritativeKey); repairErr != nil {
				cacheSafe = false
				common.SysLog("failed to fence token quota cache: " + repairErr.Error())
			}
		}
	}
	if cacheSafe {
		if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityToken, tokenId, getTokenCacheKey(authoritativeKey), mutationID); err != nil {
			common.SysLog("failed to complete token quota cache repair: " + err.Error())
		}
	}
	return nil
}

func increaseTokenQuotaWithCacheRepair(id int, quota int) (mutationID string, authoritativeKey string, err error) {
	err = DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Token{}).Where("id = ?", id).Updates(
			map[string]interface{}{
				"remain_quota":  gorm.Expr("remain_quota + ?", quota),
				"used_quota":    gorm.Expr("used_quota - ?", quota),
				"accessed_time": common.GetTimestamp(),
			},
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		authoritativeKey, err = tokenKeyByIDTx(tx, id)
		if err != nil {
			return err
		}
		mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityToken, id, getTokenCacheKey(authoritativeKey))
		return err
	})
	return mutationID, authoritativeKey, err
}

func increaseTokenQuota(id int, quota int) (err error) {
	if quota == 0 {
		return nil
	}
	result := DB.Model(&Token{}).Where("id = ?", id).Updates(
		map[string]interface{}{
			"remain_quota":  gorm.Expr("remain_quota + ?", quota),
			"used_quota":    gorm.Expr("used_quota - ?", quota),
			"accessed_time": common.GetTimestamp(),
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

func DecreaseTokenQuota(id int, key string, quota int) (err error) {
	if quota < 0 || quota > common.MaxQuota {
		return fmt.Errorf("quota 超出范围: %d", quota)
	}
	if quota == 0 {
		return nil
	}
	// Keep financial token mutations synchronous and durable.  Only usage
	// aggregates use the in-memory batch updater, so a process restart cannot
	// erase an accepted token charge.
	mutationID, authoritativeKey, err := decreaseTokenQuota(id, quota)
	if err != nil {
		return err
	}
	cacheSafe := true
	if common.RedisEnabled {
		result, cacheErr := cacheApplyTokenQuotaDelta(id, authoritativeKey, int64(-quota))
		if cacheErr != nil || result != cacheQuotaOK {
			if cacheErr == nil {
				cacheErr = ErrQuotaCacheMiss
			}
			common.SysLog("failed to decrease token quota cache: " + cacheErr.Error())
			if repairErr := invalidateTokenCacheForMutation(authoritativeKey); repairErr != nil {
				cacheSafe = false
				common.SysLog("failed to fence token quota cache: " + repairErr.Error())
			}
		}
	}
	if cacheSafe {
		if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityToken, id, getTokenCacheKey(authoritativeKey), mutationID); err != nil {
			common.SysLog("failed to complete token quota cache repair: " + err.Error())
		}
	}
	return nil
}

func decreaseTokenQuota(id int, quota int) (mutationID string, authoritativeKey string, err error) {
	if quota == 0 {
		return "", "", nil
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Token{}).Where("id = ?", id).Updates(
			map[string]interface{}{
				"remain_quota":  gorm.Expr("remain_quota - ?", quota),
				"used_quota":    gorm.Expr("used_quota + ?", quota),
				"accessed_time": common.GetTimestamp(),
			},
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		authoritativeKey, err = tokenKeyByIDTx(tx, id)
		if err != nil {
			return err
		}
		mutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityToken, id, getTokenCacheKey(authoritativeKey))
		return err
	})
	return mutationID, authoritativeKey, err
}

// CountUserTokens returns total number of tokens for the given user, used for pagination
func CountUserTokens(userId int) (int64, error) {
	var total int64
	err := DB.Model(&Token{}).Where("user_id = ?", userId).Count(&total).Error
	return total, err
}

// BatchDeleteTokens 删除指定用户的一组令牌，返回成功删除数量
func BatchDeleteTokens(ids []int, userId int) (int, error) {
	if len(ids) == 0 {
		return 0, errors.New("ids 不能为空！")
	}

	tx := DB.Begin()
	if tx.Error != nil {
		return 0, tx.Error
	}
	defer tx.Rollback()

	var tokens []Token
	if err := tx.Where("user_id = ? AND id IN (?)", userId, ids).Find(&tokens).Error; err != nil {
		return 0, err
	}
	if err := invalidateTokensCache(tokens); err != nil {
		common.SysLog("failed to invalidate token cache before batch delete: " + err.Error())
	}

	// Keep every post-commit cache action in the same transaction as the
	// deletion.  Calling enqueueQuotaCacheRepair through the global DB here
	// would contend with this open transaction (and deadlock SQLite), so stage
	// each marker on tx and complete it only after Redis confirms the fence.
	mutationIDs := make(map[int]string, len(tokens))
	cacheKeys := make(map[int]string, len(tokens))
	for i := range tokens {
		mutationID, key, err := stageTokenCacheRepairForMutationTx(tx, tokens[i].Id, tokens[i].Key)
		if err != nil {
			return 0, err
		}
		mutationIDs[tokens[i].Id] = mutationID
		cacheKeys[tokens[i].Id] = key
	}

	result := tx.Where("user_id = ? AND id IN (?)", userId, ids).Delete(&Token{})
	if result.Error != nil {
		return 0, result.Error
	}

	if err := tx.Commit().Error; err != nil {
		return 0, err
	}

	for _, token := range tokens {
		key := cacheKeys[token.Id]
		mutationID := mutationIDs[token.Id]
		if key == "" || !common.RedisEnabled {
			continue
		}
		cacheKey := getTokenCacheKey(key)
		if cacheErr := invalidateTokenCacheForMutation(key); cacheErr != nil {
			common.SysLog("failed to invalidate token cache after batch delete: " + cacheErr.Error())
			continue
		}
		if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityToken, token.Id, cacheKey, mutationID); err != nil {
			common.SysLog("failed to complete token batch-delete cache repair: " + err.Error())
		}
	}

	return int(result.RowsAffected), nil
}

func GetTokenKeysByIds(ids []int, userId int) ([]Token, error) {
	var tokens []Token
	err := DB.Select("id", mainKeyColumn(DB), "key_ciphertext", "key_hash").
		Where("user_id = ? AND id IN (?)", userId, ids).
		Find(&tokens).Error
	return tokens, err
}

// InvalidateUserTokensCache 清理指定用户所有令牌在 Redis 中的缓存，
// 配合 InvalidateUserCache 使用，可在用户被禁用/删除时立即阻断其令牌的请求。
// 下一次请求将从数据库重新加载令牌及用户状态，从而立即识别出被禁用的用户。
func InvalidateUserTokensCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	if userId <= 0 {
		return errors.New("userId 无效")
	}
	var tokens []Token
	if err := DB.Unscoped().
		Select("id", mainKeyColumn(DB), "key_ciphertext", "key_hash").
		Where("user_id = ?", userId).
		Find(&tokens).Error; err != nil {
		return err
	}
	return invalidateTokensCache(tokens)
}

func invalidateTokensCache(tokens []Token) error {
	if !common.RedisEnabled {
		return nil
	}
	var firstErr error
	for _, t := range tokens {
		if t.Key == "" {
			continue
		}
		if err := invalidateTokenCacheForMutation(t.Key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
