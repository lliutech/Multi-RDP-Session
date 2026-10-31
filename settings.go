//go:build windows

// settings.go —— 远程设置项
//
// 每一项对应 HKLM 下的一个 REG_DWORD,可单独修改、单独还原。
// 首次修改前记录原值(含"原本不存在")到 settings.json;
// 若该值已被 unlock 记录(state.json),以 state.json 为准。

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const (
	skBool    = iota // 1 = 开
	skBoolInv        // 0 = 开(如 fDenyTSConnections=0 表示启用远程桌面)
	skInt            // 整数
	skEnum           // 枚举
)

// 分组
const (
	secConn = iota
	secSession
	secRedirect
)

func sectionTitle(s int) string {
	switch s {
	case secConn:
		return T("连接与安全", "Connection & security")
	case secSession:
		return T("会话与并发", "Sessions & concurrency")
	default:
		return T("设备重定向", "Device redirection")
	}
}

type enumOpt struct {
	V      uint32
	Zh, En string
}

func (o enumOpt) label() string {
	if curLang == langEN {
		return o.En
	}
	return o.Zh
}

type settingDef struct {
	Key, Name    string
	Zh, En       string
	ZhDesc, EnDe string
	Kind         int
	Sec          int
	Enum         []enumOpt
	UnitZh       string
	UnitEn       string
	Ms           bool // 单位是毫秒,展示时追加 ms
	Min, Max     uint32
}

func (d settingDef) label() string {
	if curLang == langEN {
		return d.En
	}
	return d.Zh
}

func (d settingDef) desc() string {
	if curLang == langEN {
		return d.EnDe
	}
	return d.ZhDesc
}

func (d settingDef) unit() string {
	if curLang == langEN {
		return d.UnitEn
	}
	return d.UnitZh
}

var settingsDefs = []settingDef{
	{tsKey, "fDenyTSConnections", "启用远程桌面", "Enable Remote Desktop",
		"关闭后任何设备都无法通过 RDP 连接本机", "When off, no device can connect to this PC over RDP",
		skBoolInv, secConn, nil, "", "", false, 0, 1},

	{tsKey, "UserAuthentication", "要求网络级身份验证 (NLA)", "Require Network Level Authentication (NLA)",
		"连接建立前先完成身份验证,可显著降低 3389 爆破风险", "Authenticate before the session is created; greatly reduces 3389 brute-force risk",
		skBool, secConn, nil, "", "", false, 0, 1},

	{tsKey, "SecurityLayer", "安全层", "Security layer",
		"0 = RDP 安全层,1 = 协商,2 = SSL(TLS)", "0 = RDP security layer, 1 = negotiate, 2 = SSL (TLS)",
		skEnum, secConn, []enumOpt{{0, "RDP 安全层", "RDP security"}, {1, "协商", "Negotiate"}, {2, "SSL/TLS", "SSL/TLS"}},
		"", "", false, 0, 2},

	{tsKey, "MinEncryptionLevel", "最低加密级别", "Minimum encryption level",
		"1 低 / 2 客户端兼容 / 3 高 / 4 FIPS", "1 low / 2 client compatible / 3 high / 4 FIPS",
		skEnum, secConn, []enumOpt{{1, "低", "Low"}, {2, "客户端兼容", "Client compatible"}, {3, "高", "High"}, {4, "FIPS", "FIPS"}},
		"", "", false, 1, 4},

	{tsKey, "PortNumber", "监听端口", "Listening port",
		"默认 3389;改端口后需同步放行防火墙", "Default 3389; open the new port in the firewall after changing it",
		skInt, secConn, nil, "端口", "port", false, 1, 65535},

	{tsKey, "KeepAliveInterval", "保活间隔", "Keep-alive interval",
		"0 = 关闭;空闲时定期探测,防止 NAT 映射被回收", "0 = off; probes while idle so NAT mappings are not dropped",
		skInt, secConn, nil, "毫秒", "ms", true, 0, 86400000},

	{polKey, "fPromptForPassword", "总是提示凭据", "Always prompt for credentials",
		"即使已保存密码也强制重新输入", "Ask again even when credentials are already saved",
		skBool, secConn, nil, "", "", false, 0, 1},

	{polKey, "fSingleSessionPerUser", "允许同一账户多会话(策略层)", "Allow multiple sessions per user (policy)",
		"0 = 同一账户可同时本地 + 远程登录", "0 = the same account may be logged on locally and remotely at once",
		skBoolInv, secSession, nil, "", "", false, 0, 1},

	{tsKey, "fSingleSessionPerUser", "允许同一账户多会话(产品层)", "Allow multiple sessions per user (product)",
		"与策略层同名,两层都要放开才生效", "Same name at product level; both layers must allow it",
		skBoolInv, secSession, nil, "", "", false, 0, 1},

	{polKey, "MaxConnectionCount", "最大连接数", "Max connections",
		"单监听允许的并发连接上限,解锁时设为 999999", "Concurrent connection limit per listener; unlock sets 999999",
		skInt, secSession, nil, "个", "", false, 1, 999999},

	{polKey, "MaxIdleTime", "空闲超时", "Idle timeout",
		"0 = 永不;超过该时长自动断开", "0 = never; the session is disconnected after this much idle time",
		skInt, secSession, nil, "毫秒", "ms", true, 0, 86400000},

	{polKey, "fDisableClip", "禁用剪贴板重定向", "Disable clipboard redirection",
		"开启后远程会话无法与本地互相复制粘贴", "When on, copy and paste between local and remote is blocked",
		skBool, secRedirect, nil, "", "", false, 0, 1},

	{polKey, "fDisableCdm", "禁用驱动器重定向", "Disable drive redirection",
		"开启后远程会话看不到本地磁盘", "When on, local drives are hidden from the remote session",
		skBool, secRedirect, nil, "", "", false, 0, 1},

	{polKey, "fDisablePNPRedir", "禁用打印机重定向", "Disable printer redirection",
		"开启后不把本地打印机映射进远程会话", "When on, local printers are not mapped into the remote session",
		skBool, secRedirect, nil, "", "", false, 0, 1},
}

func settingID(key, name string) string { return key + "|" + name }

type settingsStore struct {
	Items map[string]dwordState `json:"items"`
}

func settingsPath() string { return filepath.Join(stateDir(), "settings.json") }

func loadSettingsStore() *settingsStore {
	s := &settingsStore{Items: map[string]dwordState{}}
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		return s
	}
	if json.Unmarshal(b, s) != nil || s.Items == nil {
		s.Items = map[string]dwordState{}
	}
	return s
}

func saveSettingsStore(s *settingsStore) error {
	if err := os.MkdirAll(stateDir(), 0755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(settingsPath(), b, 0644)
}

func readSetting(key, name string) (uint32, bool) {
	v, ok, _ := regGetDWORD(key, name)
	return v, ok
}

// originOf 返回可还原的原值:优先采用 unlock 记录的原始值。
func originOf(key, name string) (dwordState, bool) {
	if st := loadState(); st != nil {
		if m, ok := st.Policy[key]; ok {
			if ds, ok2 := m[name]; ok2 {
				return ds, true
			}
		}
	}
	s := loadSettingsStore()
	ds, ok := s.Items[settingID(key, name)]
	return ds, ok
}

// applySetting 写入新值,并在首次修改前记录原值。
func applySetting(d settingDef, v uint32) (old uint32, oldOK bool, recorded bool, err error) {
	old, oldOK = readSetting(d.Key, d.Name)
	if _, exists := originOf(d.Key, d.Name); !exists {
		s := loadSettingsStore()
		s.Items[settingID(d.Key, d.Name)] = dwordState{Existed: oldOK, Value: old}
		if err = saveSettingsStore(s); err != nil {
			return old, oldOK, false, fmt.Errorf(T("记录原值失败: %v", "failed to save original value: %v"), err)
		}
		recorded = true
	}
	if err = regSetDWORD(d.Key, d.Name, v); err != nil {
		return old, oldOK, recorded, fmt.Errorf(T("写入注册表失败: %v", "registry write failed: %v"), err)
	}
	return old, oldOK, recorded, nil
}

// restoreSetting 把单项还原到记录的原值。
func restoreSetting(key, name string) (dwordState, bool, error) {
	ds, ok := originOf(key, name)
	if !ok {
		return dwordState{}, false, fmt.Errorf(T("该项没有原值记录,无法还原", "no original value recorded for this item"))
	}
	var err error
	if ds.Existed {
		err = regSetDWORD(key, name, ds.Value)
	} else {
		err = regDelValue(key, name)
	}
	if err != nil {
		return ds, true, err
	}
	s := loadSettingsStore()
	delete(s.Items, settingID(key, name))
	if e := saveSettingsStore(s); e != nil {
		return ds, true, fmt.Errorf(T("已还原,但清理记录失败: %v", "restored, but failed to clear the record: %v"), e)
	}
	return ds, true, nil
}

// restoreAllSettings 还原本工具记录过的全部设置项。
func restoreAllSettings() (restored int, errs []string) {
	s := loadSettingsStore()
	for id := range s.Items {
		var key, name string
		for i := 0; i < len(id); i++ {
			if id[i] == '|' {
				key, name = id[:i], id[i+1:]
				break
			}
		}
		if key == "" {
			continue
		}
		if _, _, e := restoreSetting(key, name); e != nil {
			errs = append(errs, name+": "+e.Error())
			continue
		}
		restored++
	}
	return
}

func enumLabel(d settingDef, v uint32) string {
	for _, o := range d.Enum {
		if o.V == v {
			return o.label()
		}
	}
	return strconv.FormatUint(uint64(v), 10)
}

func boolOn(d settingDef, v uint32) bool {
	if d.Kind == skBoolInv {
		return v == 0
	}
	return v != 0
}

func boolValue(d settingDef, on bool) uint32 {
	if d.Kind == skBoolInv {
		if on {
			return 0
		}
		return 1
	}
	if on {
		return 1
	}
	return 0
}
