//go:build windows

// status.go —— GUI 信息区数据:当前系统版本 / 工具针对版本 / termsrv 补丁状态

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

type sysStatus struct {
	OsVersion  string // "Windows 11 25H2 · Build 26200.9168 · Pro for Workstations"
	OsShort    string // "Windows 11 25H2"
	OsSub      string // "Build 26200.9168 · 专业工作站版"
	TermsrvVer string // "10.0.26100.9949"
	State      int    // psUnsupported / psUnlocked / psUnlockable
	Detail     string // State 为 psUnsupported 时的原因
	RDPEnabled bool
	PatchText  string // "补丁点 3/3 唯一命中" / "已解锁 3/3"
	PolicyText string // "策略已放开" / "策略层仍限制单会话"
	HasState   bool   // 是否存在解锁操作记录(决定"一键还原"是否可用)
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

func osVersionText() string {
	short, sub := osVersionParts()
	if sub == "" {
		return short
	}
	return short + " · " + sub
}

// osVersionParts 拆成"短标题 + 副标题",便于 UI 分两行排版而不被截断。
func osVersionParts() (short, sub string) {
	cv := `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	display := regGetString(cv, "DisplayVersion")
	build := regGetString(cv, "CurrentBuildNumber")
	edition := regGetString(cv, "EditionID")
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
	ed := editionLabel(edition)
	if ed == "" {
		ed = "?"
	}
	short = fmt.Sprintf("%s %s", family, display)
	sub = fmt.Sprintf("Build %s.%d · %s", build, ubr, ed)
	return
}

func gatherStatus() sysStatus {
	var st sysStatus
	st.OsShort, st.OsSub = osVersionParts()
	st.OsVersion = st.OsShort + " · " + st.OsSub
	if v, ok, _ := regGetDWORD(tsKey, "fDenyTSConnections"); ok && v == 0 {
		st.RDPEnabled = true
	}

	// 策略层摘要
	polSingle, polOK1, _ := regGetDWORD(polKey, "fSingleSessionPerUser")
	tsSingle, polOK2, _ := regGetDWORD(tsKey, "fSingleSessionPerUser")
	maxConn, polOK3, _ := regGetDWORD(polKey, "MaxConnectionCount")
	switch {
	case polOK1 && polSingle == 0 && polOK2 && tsSingle == 0 && polOK3 && maxConn > 1:
		st.PolicyText = T("策略已放开 · MaxConn 999999", "policy relaxed · MaxConn 999999")
	case polOK1 || polOK2:
		st.PolicyText = T("策略层仍限制单用户单会话", "policy still limits one session per user")
	default:
		st.PolicyText = T("策略未做任何修改", "policy untouched")
	}

	data, err := os.ReadFile(termsrvPath)
	if err != nil {
		st.State = psUnsupported
		st.Detail = T("读取 termsrv.dll 失败: ", "reading termsrv.dll failed: ") + err.Error()
		return st
	}
	st.TermsrvVer = shortVer(dllFileVersion(termsrvPath))
	st.HasState = loadState() != nil
	sr := scanPatches(data)
	switch {
	case sr.AlreadyPatched == len(patchDefs):
		st.State = psUnlocked
		st.PatchText = fmt.Sprintf(T("补丁点 已解锁 %d/%d", "patched %d/%d"), sr.AlreadyPatched, len(patchDefs))
	case len(sr.Sites) == len(patchDefs):
		st.State = psUnlockable
		st.PatchText = fmt.Sprintf(T("补丁点 %d/%d 唯一命中(%s),可自动修补", "%d/%d patch sites matched (%s), ready to patch"),
			len(sr.Sites), len(patchDefs), sr.Method)
	default:
		st.State = psUnsupported
		st.Detail = fmt.Sprintf(T("仅命中 %d/%d 处补丁点(已解锁 %d 处)", "only %d/%d patch sites matched (%d already patched)"),
			len(sr.Sites), len(patchDefs), sr.AlreadyPatched)
		st.PatchText = fmt.Sprintf(T("补丁点 %d/%d", "patch sites %d/%d"), len(sr.Sites), len(patchDefs))
	}
	return st
}
