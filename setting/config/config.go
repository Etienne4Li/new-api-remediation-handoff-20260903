package config

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// ConfigManager 统一管理所有配置
type ConfigManager struct {
	configs map[string]interface{}
	mutex   sync.RWMutex
}

// ConfigSnapshotProvider lets a registered module expose a detached,
// consistent value for persistence and status responses. The legacy
// reflection path reads fields directly from the registered pointer, which
// is unsafe for hot-reloaded settings that use copy-on-write or an internal
// lock.
type ConfigSnapshotProvider interface {
	ConfigSnapshot() interface{}
}

// ConfigMapUpdater lets a registered module validate and publish a group of
// fields atomically. Modules that implement this interface own their
// synchronization; ConfigManager does not reflect writes into their live
// object.
type ConfigMapUpdater interface {
	ValidateConfigMap(map[string]string) error
	UpdateConfigMap(map[string]string) error
}

// ConfigSnapshotRestorer is an optional stronger rollback hook for custom
// configuration modules.  ConfigManager falls back to replaying the detached
// field map when a module does not implement it, but modules with side effects
// or non-reflection state can restore their exact generation here.
type ConfigSnapshotRestorer interface {
	RestoreConfigSnapshot(interface{}) error
}

type stagedConfig struct {
	name         string
	config       interface{}
	fields       []stagedConfigField
	updater      ConfigMapUpdater
	configMap    map[string]string
	before       interface{}
	beforeValues map[string]string
}

var GlobalConfig = NewConfigManager()

func NewConfigManager() *ConfigManager {
	return &ConfigManager{
		configs: make(map[string]interface{}),
	}
}

// Register 注册一个配置模块
func (cm *ConfigManager) Register(name string, config interface{}) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.configs[name] = config
}

// Get 获取指定配置模块
func (cm *ConfigManager) Get(name string) interface{} {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	return cm.configs[name]
}

// LoadFromDB 从数据库加载配置
func (cm *ConfigManager) LoadFromDB(options map[string]string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// Stage every registered module first. No module is published until all
	// persisted values have parsed successfully; a bad value in one module
	// therefore cannot leave the process with a cross-module half refresh.
	names := make([]string, 0, len(cm.configs))
	for name := range cm.configs {
		names = append(names, name)
	}
	sort.Strings(names)
	staged := make([]stagedConfig, 0, len(names))
	var firstErr error
	for _, name := range names {
		config := cm.configs[name]
		prefix := name + "."
		configMap := make(map[string]string)

		// 收集属于此配置的所有选项
		for key, value := range options {
			if strings.HasPrefix(key, prefix) {
				configKey := strings.TrimPrefix(key, prefix)
				configMap[configKey] = value
			}
		}

		// 如果找到配置项，则更新配置
		if len(configMap) > 0 {
			// Capture a detached pre-commit snapshot before any module is
			// published.  It is used to undo both already-committed modules and
			// an updater that returns an error after mutating its live object.
			var before interface{}
			if provider, ok := config.(ConfigSnapshotProvider); ok {
				before = provider.ConfigSnapshot()
			} else {
				before = nil
			}
			var beforeValues map[string]string
			var snapshotErr error
			if before != nil {
				beforeValues, snapshotErr = configToMap(before)
			} else {
				beforeValues, snapshotErr = configToMap(config)
			}
			if snapshotErr != nil {
				common.SysError("failed to snapshot config " + name + ": " + snapshotErr.Error())
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: snapshot: %w", name, snapshotErr)
				}
				continue
			}
			if updater, ok := config.(ConfigMapUpdater); ok {
				if err := updater.ValidateConfigMap(configMap); err != nil {
					common.SysError("failed to update config " + name + ": " + err.Error())
					if firstErr == nil {
						firstErr = fmt.Errorf("%s: %w", name, err)
					}
					continue
				}
				staged = append(staged, stagedConfig{name: name, config: config, updater: updater, configMap: configMap, before: before, beforeValues: beforeValues})
				continue
			}
			fields, err := stageConfigFields(config, configMap)
			if err != nil {
				common.SysError("failed to update config " + name + ": " + err.Error())
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", name, err)
				}
				continue
			}
			staged = append(staged, stagedConfig{name: name, config: config, fields: fields, before: before, beforeValues: beforeValues})
		}
	}
	if firstErr != nil {
		return firstErr
	}
	committed := make([]stagedConfig, 0, len(staged))
	for _, update := range staged {
		var err error
		if update.updater != nil {
			err = update.updater.UpdateConfigMap(update.configMap)
		} else {
			err = commitConfigFields(update.fields)
		}
		if err != nil {
			// An updater is allowed to perform arbitrary work and may have
			// changed its live value before returning an error. Restore the
			// failing module first, then all prior commits in reverse order.
			rollbackErr := restoreStagedConfigs(append(committed, update))
			if rollbackErr != nil {
				return fmt.Errorf("%s: commit config: %w (rollback: %v)", update.name, err, rollbackErr)
			}
			return fmt.Errorf("%s: commit config: %w", update.name, err)
		}
		committed = append(committed, update)
	}

	return nil
}

// restoreStagedConfigs restores a set of modules in reverse commit order.
// The caller holds ConfigManager.mutex, so registration cannot change while
// rollback is running.  All errors are retained as one diagnostic; callers
// must treat any rollback error as a potentially inconsistent runtime state.
func restoreStagedConfigs(staged []stagedConfig) error {
	var firstErr error
	for index := len(staged) - 1; index >= 0; index-- {
		update := staged[index]
		if err := restoreStagedConfig(update); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", update.name, err)
		}
	}
	return firstErr
}

func restoreStagedConfig(update stagedConfig) error {
	if update.updater != nil {
		if update.before != nil {
			if restorer, ok := update.updater.(ConfigSnapshotRestorer); ok {
				return restorer.RestoreConfigSnapshot(update.before)
			}
		}
		// ConfigMapUpdater is expected to publish a complete map atomically;
		// replaying the detached pre-commit map is the generic fallback.
		return update.updater.UpdateConfigMap(update.beforeValues)
	}
	fields, err := stageConfigFields(update.config, update.beforeValues)
	if err != nil {
		return err
	}
	return commitConfigFields(fields)
}

// SaveToDB 将配置保存到数据库
func (cm *ConfigManager) SaveToDB(updateFunc func(key, value string) error) error {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	for name, config := range cm.configs {
		configMap, err := configToMap(config)
		if err != nil {
			return err
		}

		for key, value := range configMap {
			dbKey := name + "." + key
			if err := updateFunc(dbKey, value); err != nil {
				return err
			}
		}
	}

	return nil
}

// 辅助函数：将配置对象转换为map
func configToMap(config interface{}) (map[string]string, error) {
	if provider, ok := config.(ConfigSnapshotProvider); ok {
		// Providers return a detached value (normally a struct), so the recursive
		// call takes the existing reflection path without touching live state.
		return configToMap(provider.ConfigSnapshot())
	}

	result := make(map[string]string)

	val := reflect.ValueOf(config)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return nil, nil
	}

	typ := val.Type()
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := typ.Field(i)

		// 跳过未导出字段
		if !fieldType.IsExported() {
			continue
		}

		// 获取json标签作为键名
		key := fieldType.Tag.Get("json")
		if key == "" || key == "-" {
			key = fieldType.Name
		}

		// 处理不同类型的字段
		var strValue string
		switch field.Kind() {
		case reflect.String:
			strValue = field.String()
		case reflect.Bool:
			strValue = strconv.FormatBool(field.Bool())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			strValue = strconv.FormatInt(field.Int(), 10)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			strValue = strconv.FormatUint(field.Uint(), 10)
		case reflect.Float32, reflect.Float64:
			strValue = strconv.FormatFloat(field.Float(), 'f', -1, 64)
		case reflect.Ptr:
			// 处理指针类型：如果非 nil，序列化指向的值
			if !field.IsNil() {
				bytes, err := common.Marshal(field.Interface())
				if err != nil {
					return nil, err
				}
				strValue = string(bytes)
			} else {
				// nil 指针序列化为 "null"
				strValue = "null"
			}
		case reflect.Map, reflect.Slice, reflect.Struct:
			// 复杂类型使用JSON序列化
			bytes, err := common.Marshal(field.Interface())
			if err != nil {
				return nil, err
			}
			strValue = string(bytes)
		default:
			// 跳过不支持的类型
			continue
		}

		result[key] = strValue
	}

	return result, nil
}

// configFieldKey returns the persisted key for a struct field.  The part
// after the first comma contains encoding options (for example omitempty) and
// is not part of the database key.
func configFieldKey(field reflect.StructField) string {
	key := field.Tag.Get("json")
	if comma := strings.IndexByte(key, ','); comma >= 0 {
		key = key[:comma]
	}
	if key == "" || key == "-" {
		return field.Name
	}
	return key
}

// parseConfigField parses one persisted value into a new reflect.Value.  It
// never mutates the live destination, which lets updateConfigFromMap publish
// a complete configuration only after every supplied field is valid.
func parseConfigField(fieldType reflect.Type, raw string) (reflect.Value, error) {
	parsed := reflect.New(fieldType).Elem()
	trimmed := strings.TrimSpace(raw)

	switch parsed.Kind() {
	case reflect.String:
		parsed.SetString(raw)
	case reflect.Bool:
		value, err := strconv.ParseBool(trimmed)
		if err != nil {
			return reflect.Value{}, err
		}
		parsed.SetBool(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		bits := parsed.Type().Bits()
		value, err := strconv.ParseInt(trimmed, 10, bits)
		if err != nil {
			// Older persisted options sometimes contain an integral float string
			// (for example "2.000000").  Preserve that compatibility only when
			// the value is finite, integral, and within the destination range.
			floatValue, floatErr := strconv.ParseFloat(trimmed, 64)
			if floatErr != nil || math.IsNaN(floatValue) || math.IsInf(floatValue, 0) || floatValue != math.Trunc(floatValue) {
				return reflect.Value{}, err
			}
			value, err = strconv.ParseInt(strconv.FormatFloat(floatValue, 'f', 0, 64), 10, bits)
			if err != nil {
				return reflect.Value{}, err
			}
		}
		parsed.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		bits := parsed.Type().Bits()
		value, err := strconv.ParseUint(trimmed, 10, bits)
		if err != nil {
			floatValue, floatErr := strconv.ParseFloat(trimmed, 64)
			if floatErr != nil || math.IsNaN(floatValue) || math.IsInf(floatValue, 0) || floatValue < 0 || floatValue != math.Trunc(floatValue) {
				return reflect.Value{}, err
			}
			value, err = strconv.ParseUint(strconv.FormatFloat(floatValue, 'f', 0, 64), 10, bits)
			if err != nil {
				return reflect.Value{}, err
			}
		}
		parsed.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(trimmed, parsed.Type().Bits())
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			if err == nil {
				err = fmt.Errorf("non-finite value")
			}
			return reflect.Value{}, err
		}
		parsed.SetFloat(value)
	case reflect.Ptr:
		if trimmed == "null" {
			return parsed, nil
		}
		parsed.Set(reflect.New(fieldType.Elem()))
		if err := common.Unmarshal([]byte(raw), parsed.Interface()); err != nil {
			return reflect.Value{}, err
		}
	case reflect.Map, reflect.Slice, reflect.Struct, reflect.Interface:
		if err := common.Unmarshal([]byte(raw), parsed.Addr().Interface()); err != nil {
			return reflect.Value{}, err
		}
	default:
		return reflect.Value{}, fmt.Errorf("unsupported kind %s", parsed.Kind())
	}
	return parsed, nil
}

type stagedConfigField struct {
	field reflect.Value
	value reflect.Value
	key   string
}

// stageConfigFields parses every supplied field without mutating the live
// configuration. Callers can use this as a validation/preflight step before a
// database transaction, then commit the complete set in one pass.
func stageConfigFields(config interface{}, configMap map[string]string) ([]stagedConfigField, error) {
	val := reflect.ValueOf(config)
	if !val.IsValid() || val.Kind() != reflect.Ptr || val.IsNil() {
		return nil, fmt.Errorf("config must be a non-nil pointer to struct")
	}
	val = val.Elem()
	if val.Kind() != reflect.Struct {
		return nil, fmt.Errorf("config must point to a struct")
	}

	// Parse every field into isolated values first.  Do not mutate the live
	// object until all fields have passed validation.
	stagedFields := make([]stagedConfigField, 0, len(configMap))
	typ := val.Type()
	for i := 0; i < val.NumField(); i++ {
		fieldType := typ.Field(i)
		if !fieldType.IsExported() {
			continue
		}
		key := configFieldKey(fieldType)
		raw, ok := configMap[key]
		if !ok {
			continue
		}
		field := val.Field(i)
		if !field.CanSet() {
			return nil, fmt.Errorf("field %s cannot be set", key)
		}
		parsed, err := parseConfigField(field.Type(), raw)
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", key, err)
		}
		stagedFields = append(stagedFields, stagedConfigField{field: field, value: parsed, key: key})
	}
	return stagedFields, nil
}

// commitConfigFields publishes values that have already passed the isolated
// parsing stage. Registered settings may expose stable pointers (for example
// RWMap instances shared by hot-path accessors); update the pointed-to value in
// place when possible instead of swapping the pointer out from under readers.
func commitConfigFields(stagedFields []stagedConfigField) error {
	// Commit staged values.  Registered settings may expose stable pointers
	// (for example RWMap instances shared by hot-path accessors); update the
	// pointed-to value in place when possible instead of swapping the pointer
	// out from under those accessors.
	for _, staged := range stagedFields {
		if staged.field.Kind() == reflect.Ptr && !staged.field.IsNil() && !staged.value.IsNil() {
			encoded, err := common.Marshal(staged.value.Interface())
			if err != nil {
				return fmt.Errorf("encode value for %s: %w", staged.key, err)
			}
			if err := common.Unmarshal(encoded, staged.field.Interface()); err != nil {
				return fmt.Errorf("commit value for %s: %w", staged.key, err)
			}
			continue
		}
		staged.field.Set(staged.value)
	}
	return nil
}

// 辅助函数：从map更新配置对象
func updateConfigFromMap(config interface{}, configMap map[string]string) error {
	stagedFields, err := stageConfigFields(config, configMap)
	if err != nil {
		return err
	}
	return commitConfigFields(stagedFields)
}

// ValidateConfigFromMap validates persisted values without changing the live
// configuration. It is intentionally separate from UpdateConfigFromMap so
// option persistence can reject malformed values before committing them to
// the database.
func ValidateConfigFromMap(config interface{}, configMap map[string]string) error {
	if updater, ok := config.(ConfigMapUpdater); ok {
		return updater.ValidateConfigMap(configMap)
	}
	_, err := stageConfigFields(config, configMap)
	return err
}

// ConfigToMap 将配置对象转换为map（导出函数）
func ConfigToMap(config interface{}) (map[string]string, error) {
	return configToMap(config)
}

// UpdateConfigFromMap 从map更新配置对象（导出函数）
func UpdateConfigFromMap(config interface{}, configMap map[string]string) error {
	if updater, ok := config.(ConfigMapUpdater); ok {
		return updater.UpdateConfigMap(configMap)
	}
	return updateConfigFromMap(config, configMap)
}

// ExportAllConfigs 导出所有已注册的配置为扁平结构
func (cm *ConfigManager) ExportAllConfigs() map[string]string {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	result := make(map[string]string)

	for name, cfg := range cm.configs {
		configMap, err := ConfigToMap(cfg)
		if err != nil {
			continue
		}

		// 使用 "模块名.配置项" 的格式添加到结果中
		for key, value := range configMap {
			result[name+"."+key] = value
		}
	}

	return result
}
