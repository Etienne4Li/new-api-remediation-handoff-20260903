package system_setting

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

type FetchSetting struct {
	EnableSSRFProtection   bool     `json:"enable_ssrf_protection"` // 是否启用SSRF防护
	AllowPrivateIp         bool     `json:"allow_private_ip"`
	DomainFilterMode       bool     `json:"domain_filter_mode"`         // 域名过滤模式，true: 白名单模式，false: 黑名单模式
	IpFilterMode           bool     `json:"ip_filter_mode"`             // IP过滤模式，true: 白名单模式，false: 黑名单模式
	DomainList             []string `json:"domain_list"`                // domain format, e.g. example.com, *.example.com
	IpList                 []string `json:"ip_list"`                    // CIDR format
	AllowedPorts           []string `json:"allowed_ports"`              // port range format, e.g. 80, 443, 8000-9000
	ApplyIPFilterForDomain bool     `json:"apply_ip_filter_for_domain"` // 对域名启用IP过滤（实验性）
}

var defaultFetchSetting = FetchSetting{
	EnableSSRFProtection:   true, // 默认开启SSRF防护
	AllowPrivateIp:         false,
	DomainFilterMode:       false,
	IpFilterMode:           false,
	DomainList:             []string{},
	IpList:                 []string{},
	AllowedPorts:           []string{"80", "443", "8080", "8443"},
	ApplyIPFilterForDomain: true,
}
var fetchSettingMu sync.RWMutex

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("fetch_setting", &defaultFetchSetting)
}

func (f *FetchSetting) ConfigSnapshot() interface{} {
	fetchSettingMu.RLock()
	defer fetchSettingMu.RUnlock()
	return cloneFetchSetting(defaultFetchSetting)
}

func GetFetchSetting() *FetchSetting {
	fetchSettingMu.RLock()
	defer fetchSettingMu.RUnlock()
	snapshot := cloneFetchSetting(defaultFetchSetting)
	return &snapshot
}

func cloneFetchSetting(f FetchSetting) FetchSetting {
	f.DomainList = append([]string(nil), f.DomainList...)
	f.IpList = append([]string(nil), f.IpList...)
	f.AllowedPorts = append([]string(nil), f.AllowedPorts...)
	return f
}

func (f *FetchSetting) ValidateConfigMap(values map[string]string) error {
	for key, raw := range values {
		var probe FetchSetting
		if key == "domain_list" || key == "ip_list" || key == "allowed_ports" {
			if err := common.Unmarshal([]byte(raw), mapSliceTarget(&probe, key)); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		if key == "enable_ssrf_protection" || key == "allow_private_ip" || key == "domain_filter_mode" || key == "ip_filter_mode" || key == "apply_ip_filter_for_domain" {
			if _, err := strconv.ParseBool(raw); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return nil
}

func mapSliceTarget(f *FetchSetting, key string) interface{} {
	switch key {
	case "domain_list":
		return &f.DomainList
	case "ip_list":
		return &f.IpList
	default:
		return &f.AllowedPorts
	}
}

func (f *FetchSetting) UpdateConfigMap(values map[string]string) error {
	fetchSettingMu.Lock()
	defer fetchSettingMu.Unlock()
	next := cloneFetchSetting(defaultFetchSetting)
	for key, raw := range values {
		switch key {
		case "domain_list":
			if err := common.Unmarshal([]byte(raw), &next.DomainList); err != nil {
				return err
			}
		case "ip_list":
			if err := common.Unmarshal([]byte(raw), &next.IpList); err != nil {
				return err
			}
		case "allowed_ports":
			if err := common.Unmarshal([]byte(raw), &next.AllowedPorts); err != nil {
				return err
			}
		case "enable_ssrf_protection":
			next.EnableSSRFProtection, _ = strconv.ParseBool(raw)
		case "allow_private_ip":
			next.AllowPrivateIp, _ = strconv.ParseBool(raw)
		case "domain_filter_mode":
			next.DomainFilterMode, _ = strconv.ParseBool(raw)
		case "ip_filter_mode":
			next.IpFilterMode, _ = strconv.ParseBool(raw)
		case "apply_ip_filter_for_domain":
			next.ApplyIPFilterForDomain, _ = strconv.ParseBool(raw)
		}
	}
	defaultFetchSetting = next
	return nil
}
