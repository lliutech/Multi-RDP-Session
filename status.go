//go:build windows

// status.go —— 系统状态采集
//
// 这里只采集"事实"(版本号、命中数、策略状态码),文案一律在渲染时用 T() 生成。
// 若在采集阶段就翻译,切换语言后界面会停留在旧语言 —— 卡片里的"策略已放开"
// "补丁点"等字样就是这类残留。

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// shortVer 去掉 FileVersion 后面的 "(WinBuild.xxx)" 尾巴,便于窄栏显示。
func shortVer(v string) string {
	if i := strings.IndexByte(v, ' '); i > 0 {
		return v[:i]
	}
	return v
}

// 工具特征码实测针对的版本范围
func toolTargetVer() string {
	return T("Win11 24H2/25H2 · termsrv 10.0.26100/26200(实测 10.0.26100.9949)",
		"Win11 24H2/25H2 · termsrv 10.0.26100/26200 (verified on 10.0.26100.9949)")
}

const (
	psUnsupported = iota // 特征码不匹配,无法自动补丁
	psUnlocked           // 已处于解锁状态
	psUnlockable         // 未解锁,特征码全部命中,可一键解锁
)

// 策略层状态
const (
	polUntouched = iota // 未做任何修改
	polRelaxed          // 已放开
	polLimited          // 仍限制单用户单会话
)

type sysStatus struct {
	OsShort      string // "Windows 11 25H2"
	OsBuild      string // "26200.9168"
	Edition      string // EditionID,如 "ProfessionalWorkstation"
	TermsrvVer   string // "10.0.26100.9949"
	State        int    // psUnsupported / psUnlocked / psUnlockable
	PatchN       int    // 唯一命中的补丁点数
	PatchPatched int    // 已打补丁的补丁点数
	Method       int    // 定位方式(methodExact / methodStructural)
	Policy       int    // polUntouched / polRelaxed / polLimited
	RDPEnabled   bool
	HasState     bool   // 是否存在解锁记录(决定"一键还原"是否可用)
	ReadErr      string // 读取 termsrv.dll 失败时的原始错误
}

// OsSubText 例如 "Build 26200.9168 · 专业工作站版"。
func (s sysStatus) OsSubText() string {
	ed := editionLabel(s.Edition)
	if s.OsBuild == "" {
		return ""
	}
	if ed == "" {
		return "Build " + s.OsBuild
	}
	return fmt.Sprintf("Build %s · %s", s.OsBuild, ed)
}

// PatchText 信息卡第二列副标题。
func (s sysStatus) PatchText() string {
	total := len(patchDefs)
	switch s.State {
	case psUnlocked:
		return fmt.Sprintf(T("补丁点 已解锁 %d/%d", "patch sites %d/%d patched"), s.PatchPatched, total)
	case psUnlockable:
		return fmt.Sprintf(T("补丁点 %d/%d 唯一命中(%s),可自动修补", "%d/%d patch sites matched (%s), ready to patch"),
			s.PatchN, total, methodLabel(s.Method))
	}
	return fmt.Sprintf(T("补丁点 %d/%d", "patch sites %d/%d"), s.PatchN, total)
}

// PolicyText 信息卡第三列副标题。
func (s sysStatus) PolicyText() string {
	switch s.Policy {
	case polRelaxed:
		return T("策略已放开 · MaxConn 999999", "policy relaxed · MaxConn 999999")
	case polLimited:
		return T("策略层仍限制单用户单会话", "policy still limits one session per user")
	}
	return T("策略未做任何修改", "policy untouched")
}

// DetailText 状态为"不支持"时的原因(横幅用)。
func (s sysStatus) DetailText() string {
	if s.ReadErr != "" {
		return T("读取 termsrv.dll 失败: ", "reading termsrv.dll failed: ") + s.ReadErr
	}
	if s.State != psUnsupported {
		return ""
	}
	return fmt.Sprintf(T("仅命中 %d/%d 处补丁点(已解锁 %d 处)",
		"only %d/%d patch sites matched (%d already patched)"),
		s.PatchN, len(patchDefs), s.PatchPatched)
}

func regGetString(path, name string) string {
	var k syscall.Handle
	p, _ := syscall.UTF16PtrFromString(path)
	if r, _, _ := procRegOpenKeyExW.Call(uintptr(syscall.HKEY_LOCAL_MACHINE), uintptr(unsafe.Pointer(p)), 0, uintptr(keyRead|keyWow64), uintptr(unsafe.Pointer(&k))); r != 0 {
		return ""
	}
	defer procRegCloseKey.Call(uintptr(k))
	n, _ := syscall.UTF16PtrFromString(name)
	var t, cb uint32
	cb = 520
	buf := make([]uint16, 260)
	r, _, _ := procRegQueryValueExW.Call(uintptr(k), uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&t)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&cb)))
	if r != 0 || t != 1 { // REG_SZ
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func editionLabel(id string) string {
	switch id {
	case "ProfessionalWorkstation":
		return T("专业工作站版", "Pro for Workstations")
	case "Professional":
		return T("专业版", "Pro")
	case "Enterprise":
		return T("企业版", "Enterprise")
	case "Education":
		return T("教育版", "Education")
	case "IoTEnterprise":
		return T("IoT 企业版", "IoT Enterprise")
	case "Core":
		return T("家庭版", "Home")
	case "CoreCountrySpecific":
		return T("家庭中文版", "Home China")
	case "CoreSingleLanguage":
		return T("家庭单语言版", "Home Single Language")
	}
	return id
}

func osVersionParts() (short, build, edition string) {
	cv := `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	display := regGetString(cv, "DisplayVersion")
	build = regGetString(cv, "CurrentBuildNumber")
	edition = regGetString(cv, "EditionID")
	ubr, _, _ := regGetDWORD(cv, "UBR")
	if build == "" {
		build = "?"
	}
	buildNo := 0
	fmt.Sscanf(build, "%d", &buildNo)
	family := "Windows 10"
	if buildNo >= 22000 {
		family = "Windows 11"
	}
	if display == "" {
		display = "?"
	}
	short = fmt.Sprintf("%s %s", family, display)
	build = fmt.Sprintf("%s.%d", build, ubr)
	return
}

func gatherStatus() sysStatus {
	var st sysStatus
	st.OsShort, st.OsBuild, st.Edition = osVersionParts()
	if v, ok, _ := regGetDWORD(tsKey, "fDenyTSConnections"); ok && v == 0 {
		st.RDPEnabled = true
	}

	polSingle, polOK1, _ := regGetDWORD(polKey, "fSingleSessionPerUser")
	tsSingle, polOK2, _ := regGetDWORD(tsKey, "fSingleSessionPerUser")
	maxConn, polOK3, _ := regGetDWORD(polKey, "MaxConnectionCount")
	switch {
	case polOK1 && polSingle == 0 && polOK2 && tsSingle == 0 && polOK3 && maxConn > 1:
		st.Policy = polRelaxed
	case polOK1 || polOK2:
		st.Policy = polLimited
	default:
		st.Policy = polUntouched
	}

	data, err := os.ReadFile(termsrvPath)
	if err != nil {
		st.State = psUnsupported
		st.ReadErr = err.Error()
		return st
	}
	st.TermsrvVer = shortVer(dllFileVersion(termsrvPath))
	st.HasState = loadState() != nil

	sr := scanPatches(data)
	st.PatchN = len(sr.Sites)
	st.PatchPatched = sr.AlreadyPatched
	st.Method = sr.Method
	switch {
	case sr.AlreadyPatched == len(patchDefs):
		st.State = psUnlocked
	case len(sr.Sites) == len(patchDefs):
		st.State = psUnlockable
	default:
		st.State = psUnsupported
	}
	return st
}
