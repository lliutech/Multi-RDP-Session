//go:build windows

// rdpmulti —— Windows 多用户并发会话解锁工具
//
// 图形界面(默认双击打开)+ 命令行两种用法:
//   rdpmulti check   只读体检
//   rdpmulti unlock  一键解锁
//   rdpmulti restore 一键还原
//
// 补丁原理:CDefPolicy 是 RDP 会话许可仲裁的执行核心
//   +0x63C = InUse(在用许可数), +0x638 = MaxAllowed(许可上限,客户端默认 1)
//   Query     : InUse == MaxAllowed 时返回 0xD00A0013 → jne 改 jmp
//   Logon     : ++InUse > MaxAllowed 时拒绝          → jg 改 nop nop
//   Reconnect : ++InUse > MaxAllowed 时拒绝          → jg 改 nop nop
// 三处放行后第二个会话可正常创建,两个会话互不踢出。

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// ---------------------------------------------------------------- DLL 声明

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modAdvapi32 = syscall.NewLazyDLL("advapi32.dll")
	modNetapi32 = syscall.NewLazyDLL("netapi32.dll")

	procSetConsoleOutputCP  = modKernel32.NewProc("SetConsoleOutputCP")
	procMoveFileExW         = modKernel32.NewProc("MoveFileExW")
	procOpenProcessToken    = modAdvapi32.NewProc("OpenProcessToken")
	procGetTokenInformation = modAdvapi32.NewProc("GetTokenInformation")
	procGetUserNameW        = modAdvapi32.NewProc("GetUserNameW")
	procLogonUserW          = modAdvapi32.NewProc("LogonUserW")
	procRegOpenKeyExW       = modAdvapi32.NewProc("RegOpenKeyExW")
	procRegCreateKeyExW     = modAdvapi32.NewProc("RegCreateKeyExW")
	procRegQueryValueExW    = modAdvapi32.NewProc("RegQueryValueExW")
	procRegSetValueExW      = modAdvapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW     = modAdvapi32.NewProc("RegDeleteValueW")
	procRegCloseKey         = modAdvapi32.NewProc("RegCloseKey")
	procNetUserEnum         = modNetapi32.NewProc("NetUserEnum")
	procNetLocalGroupGetMem = modNetapi32.NewProc("NetLocalGroupGetMembers")
	procNetUserSetInfo      = modNetapi32.NewProc("NetUserSetInfo")
	procNetApiBufferFree    = modNetapi32.NewProc("NetApiBufferFree")
)

func init() { procSetConsoleOutputCP.Call(65001) } // 控制台 UTF-8 输出

const (
	keyRead       = 0x20019 // KEY_READ
	keyAll        = 0xF003F // KEY_ALL_ACCESS
	keyWow64      = 0x0100  // KEY_WOW64_64KEY
	regDword      = 4       // REG_DWORD
	errMoreData   = 234
	maxPrefLen    = 0xFFFFFFFF
	tokElevation  = 20
	moveReplace   = 0x1
	moveDelayBoot = 0x4

	termsrvPath = `C:\Windows\System32\termsrv.dll`

	polKey   = `SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services`
	tsKey    = `SYSTEM\CurrentControlSet\Control\Terminal Server`
	adminSID = "*S-1-5-32-544" // Administrators 组,免受系统语言影响
)

type logger func(string, ...any)

// ---------------------------------------------------------------- 小工具

const currentProcess = ^uintptr(0) // GetCurrentProcess() 伪句柄

func utf16p(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

func utf16ToStr(p uintptr) string {
	if p == 0 {
		return ""
	}
	u := (*[1 << 15]uint16)(unsafe.Pointer(p))[:]
	n := 0
	for n < len(u) && u[n] != 0 {
		n++
	}
	return string(utf16.Decode(u[:n]))
}

func errnoOf(e error) uint32 {
	if en, ok := e.(syscall.Errno); ok {
		return uint32(en)
	}
	return 0xFFFFFFFF
}

func runCmd(name string, args ...string) (string, int) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return out.String(), code
}

func isElevated() bool {
	var h syscall.Handle
	r, _, _ := procOpenProcessToken.Call(currentProcess, 0x0008, uintptr(unsafe.Pointer(&h)))
	if r == 0 {
		return false
	}
	defer syscall.CloseHandle(h)
	var e, rl uint32
	r2, _, _ := procGetTokenInformation.Call(uintptr(h), tokElevation, uintptr(unsafe.Pointer(&e)), unsafe.Sizeof(e), uintptr(unsafe.Pointer(&rl)))
	return r2 != 0 && e != 0
}

func currentUserName() string {
	var buf [257]uint16
	n := uint32(257)
	if r, _, _ := procGetUserNameW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r != 0 {
		return syscall.UTF16ToString(buf[:])
	}
	return T("(未知)", "(unknown)")
}

func sessionIsRDP() bool { return strings.HasPrefix(os.Getenv("SESSIONNAME"), "RDP-Tcp") }

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func dllFileVersion(path string) string {
	out, code := runCmd("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("[System.Diagnostics.FileVersionInfo]::GetVersionInfo('%s').FileVersion", path))
	if code == 0 {
		if v := strings.TrimSpace(out); v != "" {
			return v
		}
	}
	return "unknown"
}

// ---------------------------------------------------------------- 注册表

func regGetDWORD(path, name string) (val uint32, existed bool, err error) {
	var k syscall.Handle
	p, _ := syscall.UTF16PtrFromString(path)
	if r, _, _ := procRegOpenKeyExW.Call(uintptr(syscall.HKEY_LOCAL_MACHINE), uintptr(unsafe.Pointer(p)), 0, uintptr(keyRead|keyWow64), uintptr(unsafe.Pointer(&k))); r != 0 {
		return 0, false, nil // 键不存在按"值不存在"处理
	}
	defer procRegCloseKey.Call(uintptr(k))
	n, _ := syscall.UTF16PtrFromString(name)
	var t, cb, v uint32
	cb = 4
	r, _, _ := procRegQueryValueExW.Call(uintptr(k), uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&t)), uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&cb)))
	if r != 0 {
		return 0, false, nil
	}
	if t != regDword {
		return 0, true, fmt.Errorf(T("%s\\%s 不是 REG_DWORD", "%s\\%s is not REG_DWORD"), path, name)
	}
	return v, true, nil
}

func regSetDWORD(path, name string, val uint32) error {
	var k syscall.Handle
	p, _ := syscall.UTF16PtrFromString(path)
	disp := uint32(0)
	if r, _, _ := procRegCreateKeyExW.Call(uintptr(syscall.HKEY_LOCAL_MACHINE), uintptr(unsafe.Pointer(p)), 0, 0, 0,
		uintptr(keyAll|keyWow64), 0, uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&disp))); r != 0 {
		return fmt.Errorf(T("RegCreateKeyEx(%s) 失败: %d", "RegCreateKeyEx(%s) failed: %d"), path, r)
	}
	defer procRegCloseKey.Call(uintptr(k))
	n, _ := syscall.UTF16PtrFromString(name)
	if r, _, _ := procRegSetValueExW.Call(uintptr(k), uintptr(unsafe.Pointer(n)), 0, regDword, uintptr(unsafe.Pointer(&val)), 4); r != 0 {
		return fmt.Errorf(T("RegSetValueEx(%s) 失败: %d", "RegSetValueEx(%s) failed: %d"), name, r)
	}
	return nil
}

func regDelValue(path, name string) error {
	var k syscall.Handle
	p, _ := syscall.UTF16PtrFromString(path)
	if r, _, _ := procRegOpenKeyExW.Call(uintptr(syscall.HKEY_LOCAL_MACHINE), uintptr(unsafe.Pointer(p)), 0, uintptr(keyAll|keyWow64), uintptr(unsafe.Pointer(&k))); r != 0 {
		return fmt.Errorf(T("RegOpenKeyEx(%s) 失败: %d", "RegOpenKeyEx(%s) failed: %d"), path, r)
	}
	defer procRegCloseKey.Call(uintptr(k))
	n, _ := syscall.UTF16PtrFromString(name)
	if r, _, _ := procRegDeleteValueW.Call(uintptr(k), uintptr(unsafe.Pointer(n))); r != 0 && r != 2 {
		return fmt.Errorf(T("RegDeleteValue(%s) 失败: %d", "RegDeleteValue(%s) failed: %d"), name, r)
	}
	return nil
}

// ---------------------------------------------------------------- 账户检测

type userInfo1 struct {
	Name, Password    uintptr
	PasswordAge, Priv uint32
	HomeDir, Comment  uintptr
	Flags, _          uint32
	ScriptPath        uintptr
}

type acctInfo struct {
	Name     string
	Flags    uint32
	Disabled bool
	BuiltIn  bool
	Blank    *bool // nil = 无法判定
	PwdDesc  string
	Locked   bool
	RemoteOK bool
}

// localUser 是 userInfo1 的安全投影:字符串在缓冲区释放前就已拷出
type localUser struct {
	Name  string
	Flags uint32
}

func listLocalUsers() ([]localUser, error) {
	var res []localUser
	var resume uint32
	var lastStatus uint32
	for {
		var buf uintptr
		var read, total uint32
		st, _, _ := procNetUserEnum.Call(0, 1, 2, uintptr(unsafe.Pointer(&buf)), maxPrefLen,
			uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
		lastStatus = uint32(st)
		if st == 0 && read > 0 {
			// 注意:必须在 NetApiBufferFree 之前把 Name 等指针内容拷成 Go 字符串,
			// 否则释放后读取的是悬垂指针(会时有时无地变成空串/乱码)
			arr := (*[1 << 16]userInfo1)(unsafe.Pointer(buf))[:read:read]
			for i := uint32(0); i < read; i++ {
				res = append(res, localUser{Name: utf16ToStr(arr[i].Name), Flags: arr[i].Flags})
			}
		}
		if buf != 0 {
			procNetApiBufferFree.Call(buf)
		}
		if st != errMoreData {
			if st != 0 {
				return res, fmt.Errorf(T("NetUserEnum 失败: %d", "NetUserEnum failed: %d"), st)
			}
			break
		}
	}
	// "成功但 0 条目"是异常情况(受限令牌/安全软件拦截),必须显式报错,否则会误报"没有账户"
	if len(res) == 0 {
		return nil, fmt.Errorf(T("NetUserEnum 返回 0 个账户(状态码 %d)——当前进程令牌可能受限。请直接双击运行本程序重试", "NetUserEnum returned 0 accounts (status %d) - the process token may be restricted.\nRun the program by double-clicking it instead"), lastStatus)
	}
	return res, nil
}

func groupMembers(group string) (map[string]bool, error) {
	out := map[string]bool{}
	var resume uintptr
	var read, total uint32
	g, _ := syscall.UTF16PtrFromString(group)
	for {
		var buf uintptr
		st, _, _ := procNetLocalGroupGetMem.Call(0, uintptr(unsafe.Pointer(g)), 3, uintptr(unsafe.Pointer(&buf)), maxPrefLen,
			uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
		if st == 0 && read > 0 {
			type m3 struct {
				Name  uintptr
				Usage uint32
			}
			arr := (*[1 << 16]m3)(unsafe.Pointer(buf))[:read:read]
			for i := uint32(0); i < read; i++ {
				full := utf16ToStr(arr[i].Name)
				if idx := strings.LastIndex(full, `\`); idx >= 0 {
					full = full[idx+1:]
				}
				out[strings.ToLower(full)] = true
			}
		}
		if buf != 0 {
			procNetApiBufferFree.Call(buf)
		}
		if st != errMoreData {
			if st != 0 {
				return out, fmt.Errorf(T("NetLocalGroupGetMembers(%s) 失败: %d", "NetLocalGroupGetMembers(%s) failed: %d"), group, st)
			}
			break
		}
	}
	return out, nil
}

// testBlankPassword 用空密码尝试网络登录,按错误码区分"有无密码"。
// 对有密码的账户会产生一次失败登录(坏密码计数 +1),所以结果要缓存。
func testBlankPassword(user string) (blank *bool, desc string, locked bool) {
	var tok syscall.Handle
	r, _, callErr := procLogonUserW.Call(utf16p(user), utf16p("."), utf16p(""), 3 /*NETWORK*/, 0, uintptr(unsafe.Pointer(&tok)))
	if r != 0 {
		syscall.CloseHandle(tok)
		t := true
		return &t, T("空密码(空密码登录成功)", "blank password (blank logon succeeded)"), false
	}
	switch errnoOf(callErr) {
	case 1326: // ERROR_LOGON_FAILURE
		f := false
		return &f, T("已设置密码", "password set"), false
	case 1327: // ERROR_ACCOUNT_RESTRICTION
		t := true
		return &t, T("空密码(受空密码网络登录限制策略约束)", "blank password (blocked by policy)"), false
	case 1330:
		return nil, T("密码已过期,无法检测", "password expired, cannot probe"), false
	case 1331:
		return nil, T("账户已禁用,无法检测", "account disabled, cannot probe"), false
	case 1907: // ERROR_PASSWORD_MUST_CHANGE
		return nil, T("首次登录需改密,无法检测", "password must be changed, cannot probe"), false
	case 1909: // ERROR_ACCOUNT_LOCKED_OUT
		return nil, T("账户已锁定,无法检测", "account locked out, cannot probe"), true
	case 1385: // 没有网络登录权限 → 改用交互式登录再试
		r2, _, e2 := procLogonUserW.Call(utf16p(user), utf16p("."), utf16p(""), 2 /*INTERACTIVE*/, 0, uintptr(unsafe.Pointer(&tok)))
		if r2 != 0 {
			syscall.CloseHandle(tok)
			t := true
			return &t, T("空密码(交互式空密码登录成功)", "blank password (interactive logon succeeded)"), false
		}
		switch errnoOf(e2) {
		case 1326:
			f := false
			return &f, T("已设置密码", "password set"), false
		case 1327:
			t := true
			return &t, T("空密码", "blank password"), false
		}
		return nil, fmt.Sprintf(T("无法检测(错误 %d)", "cannot probe (error %d)"), errnoOf(e2)), false
	default:
		return nil, fmt.Sprintf(T("无法检测(错误 %d)", "cannot probe (error %d)"), errnoOf(callErr)), false
	}
}

var builtinNames = map[string]bool{
	"administrator": true, "guest": true, "defaultaccount": true, "wdagutilityaccount": true,
}

// 密码探测缓存:探测等于一次失败登录,默认策略 10 次即锁定。
// 因此只探测有远程权限的账户,进程内复用结果,已锁定的账户不再重复探测。
type pwdProbe struct {
	blank  *bool
	desc   string
	locked bool
}

var pwdProbeCache = map[string]pwdProbe{}

func probePassword(user string, force bool) (*bool, string, bool) {
	key := strings.ToLower(user)
	if c, ok := pwdProbeCache[key]; ok {
		if !force || c.locked {
			return c.blank, c.desc, c.locked
		}
	}
	b, d, lk := testBlankPassword(user)
	pwdProbeCache[key] = pwdProbe{blank: b, desc: d, locked: lk}
	return b, d, lk
}

func analyzeAccounts(forceProbe bool) ([]acctInfo, error) {
	var res []acctInfo
	users, err := listLocalUsers()
	if err != nil {
		return nil, err
	}
	rdpMembers, _ := groupMembers("Remote Desktop Users")
	adminMembers, _ := groupMembers("Administrators")
	for _, u := range users {
		name := u.Name
		if name == "" || strings.HasSuffix(name, "$") {
			continue
		}
		a := acctInfo{
			Name:     name,
			Flags:    u.Flags,
			Disabled: u.Flags&0x2 != 0,
			BuiltIn:  builtinNames[strings.ToLower(name)],
			RemoteOK: rdpMembers[strings.ToLower(name)] || adminMembers[strings.ToLower(name)],
		}
		switch {
		case a.Disabled:
			a.PwdDesc = T("已禁用,跳过检测", "disabled, skipped")
		case !a.RemoteOK:
			a.PwdDesc = T("无远程权限,未探测", "no RDP access, not probed")
		default:
			a.Blank, a.PwdDesc, a.Locked = probePassword(name, forceProbe)
		}
		res = append(res, a)
	}
	return res, nil
}

func printAccounts(accts []acctInfo, say logger) {
	say("")
	say("  %-18s %-8s %-12s %-30s %s",
		T("账户", "ACCOUNT"), T("状态", "STATE"), T("密码", "PASSWORD"), T("说明", "DETAIL"), T("RDP权限", "RDP"))
	say("  " + strings.Repeat("-", 88))
	for _, a := range accts {
		st := T("启用", "enabled")
		if a.Disabled {
			st = T("禁用", "disabled")
		}
		pwd := T("未知", "unknown")
		if a.Blank != nil {
			if *a.Blank {
				pwd = T("[X] 空密码", "[X] blank")
			} else {
				pwd = T("[OK] 有密码", "[OK] set")
			}
		}
		perm := T("无", "no")
		if a.RemoteOK {
			perm = T("允许远程", "allowed")
		}
		bi := ""
		if a.BuiltIn {
			bi = T("(内置)", " (built-in)")
		}
		say("  %-18s %-8s %-12s %-30s %s", a.Name+bi, st, pwd, a.PwdDesc, perm)
	}
}

// 账户预检:返回(可用数, 警告数)。可用 = 启用+有密码+允许远程;警告 = 启用+允许远程但密码状态未知。
// 注意:BuiltIn 只是"显示归类",内置 Administrator 同样是合法远程账户,不能排除。
func accountGate(accts []acctInfo, say logger) (usable, warnable int) {
	for _, a := range accts {
		if a.Disabled {
			continue
		}
		if a.RemoteOK {
			if a.Blank != nil && !*a.Blank {
				usable++
			} else if a.Blank == nil {
				warnable++
			}
		}
	}
	return
}

// ---------------------------------------------------------------- 状态文件

type dwordState struct {
	Existed bool   `json:"existed"`
	Value   uint32 `json:"value"`
}

type termState struct {
	Version     string      `json:"version"`
	OriginalSHA string      `json:"original_sha256"`
	BackupPath  string      `json:"backup_path"`
	ACLPath     string      `json:"acl_save_path"`
	PatchedSHA  string      `json:"patched_sha256"`
	Sites       []patchSite `json:"sites"`

	_tmpNew   string `json:"-"`
	_origKeep string `json:"-"`
}

type stateFile struct {
	Created string                           `json:"created"`
	Policy  map[string]map[string]dwordState `json:"policy"`
	Termsrv *termState                       `json:"termsrv"`
}

func stateDir() string  { return filepath.Join(os.Getenv("ProgramData"), "rdpmulti") }
func statePath() string { return filepath.Join(stateDir(), "state.json") }
func backupDir() string { return filepath.Join(stateDir(), "backups") }

func loadState() *stateFile {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return nil
	}
	var st stateFile
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return &st
}

func saveState(st *stateFile) {
	os.MkdirAll(stateDir(), 0755)
	b, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(statePath(), b, 0644)
}

// ---------------------------------------------------------------- 服务控制

func svcStop(name string, say logger) bool {
	out, code := runCmd("net", "stop", name, "/y")
	if code == 0 || code == 2 {
		return true
	}
	say(T("     (net stop %s 退出码 %d: %s)", "     (net stop %s exited with %d: %s)"), name, code, firstLine(out))
	return false
}

func svcStart(name string) bool {
	_, code := runCmd("net", "start", name)
	return code == 0 || code == 2
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

func schedulePendingRename(src, dst string) bool {
	r, _, _ := procMoveFileExW.Call(utf16p(src), utf16p(dst), uintptr(moveDelayBoot|moveReplace))
	return r != 0
}

// (日志统一由流程函数的 say 参数输出)

// ---------------------------------------------------------------- check

func runCheck(say logger) error {
	say(T("========== rdpmulti 系统体检 ==========", "========== rdpmulti system check =========="))

	say(T("\n[1] 运行环境", "\n[1] Environment"))
	say(T("    管理员权限 : %v", "    elevated     : %v"), isElevated())
	say(T("    当前用户   : %s", "    current user : %s"), currentUserName())
	say(T("    当前会话   : %s", "    session      : %s"), orDefault(os.Getenv("SESSIONNAME"), "(未知)"))
	if sessionIsRDP() {
		say(T("    [!!] 当前正处于 RDP 会话,unlock 需要在物理控制台运行或加 --force", "    [!!] running inside an RDP session; run unlock at the console or pass --force"))
	}

	say(T("\n[2] 远程桌面开关", "\n[2] Remote Desktop switch"))
	if v, ok, _ := regGetDWORD(tsKey, "fDenyTSConnections"); ok && v == 0 {
		say(T("    [OK] 远程桌面已启用 (fDenyTSConnections=0)", "    [OK] Remote Desktop is enabled (fDenyTSConnections=0)"))
	} else {
		say(T("    [X] 远程桌面未启用,请先在 设置→系统→远程桌面 中开启", "    [X] Remote Desktop is off; enable it in Settings > System > Remote Desktop"))
	}

	say(T("\n[3] 策略层限制", "\n[3] Policy layer"))
	show := func(key, name, hint string) {
		v, ok, _ := regGetDWORD(key, name)
		if !ok {
			say(T("    %-24s 未设置          (%s)", "    %-24s not set          (%s)"), name, hint)
		} else {
			say("    %-24s = %-12d (%s)", name, v, hint)
		}
	}
	show(polKey, "fSingleSessionPerUser", T("0=允许多会话(策略层)", "0 = allow multiple sessions (policy)"))
	show(polKey, "MaxConnectionCount", T("单监听最大连接数", "max connections per listener"))
	show(tsKey, "fSingleSessionPerUser", T("0=允许多会话(产品层)", "0 = allow multiple sessions (product)"))

	say(T("\n[4] 本地账户与密码检测(空密码无法 RDP 登录)", "\n[4] Local accounts and passwords (blank passwords cannot log on over RDP)"))
	accts, acctErr := analyzeAccounts(false)
	if acctErr != nil {
		say(T("    [!!] 账户枚举失败: %v", "    [!!] account enumeration failed: %v"), acctErr)
	} else {
		printAccounts(accts, say)
	}
	usable, warnable := accountGate(accts, say)
	if acctErr == nil && usable > 0 {
		say(T("\n    [OK] 有 %d 个可用远程账户(启用+有密码+允许远程)", "\n    [OK] %d account(s) usable for RDP (enabled + password + RDP access)"), usable)
	} else if acctErr == nil && warnable > 0 {
		say(T("\n    [!!] 有 %d 个账户允许远程但密码状态未知(可能被临时锁定),解锁可继续,连接时再验证", "\n    [!!] %d account(s) have RDP access but an unknown password state (possibly locked out); unlock may proceed"), warnable)
	} else if acctErr == nil {
		say(T("\n    [X] 没有满足条件的远程账户!请为账户设置密码并加入远程组:", "\n    [X] No account can log on remotely. Set a password and add it to the RDP group:"))
		say(`        net user <账户> <新密码>`)
		say(`        net localgroup "Remote Desktop Users" <账户> /add`)
	}

	say(T("\n[5] termsrv.dll 补丁点分析", "\n[5] termsrv.dll patch sites"))
	data, err := os.ReadFile(termsrvPath)
	if err != nil {
		say(T("    [X] 读取 termsrv.dll 失败: %v", "    [X] reading termsrv.dll failed: %v"), err)
		return err
	}
	ver := dllFileVersion(termsrvPath)
	say(T("    文件版本 : %s", "    file version : %s"), ver)
	say("    SHA-256  : %s", sha256Hex(data))
	say(T("    工具收录 : %s", "    supported    : %s"), toolTargetVer())
	sr := scanPatches(data)
	if sr.AlreadyPatched == len(patchDefs) {
		say(T("    [OK] 三处补丁点均已是\"已解锁\"状态", "    [OK] all three patch sites are already patched"))
	} else if len(sr.Sites) == len(patchDefs) {
		say(T("    [OK] 三处补丁点全部唯一命中(%s定位),支持自动补丁:", "    [OK] all three patch sites matched uniquely (%s); auto-patch supported:"), sr.Method)
		for _, s := range sr.Sites {
			say("        %-24s @ %s:  %s -> %s   (%s)", s.Name, hexOff(s.Offset), s.OrigHex, s.NewHex, s.Desc)
		}
	} else {
		say(T("    [X] 仅定位到 %d/%d 处补丁点(已解锁 %d 处)。", "    [X] only %d/%d patch sites located (%d already patched)."), len(sr.Sites), len(patchDefs), sr.AlreadyPatched)
		say(T("        当前 termsrv.dll 版本(%s)可能未被本工具支持,请反馈版本号。", "        termsrv.dll (%s) may not be supported yet; please report the version."), ver)
	}

	if st := loadState(); st != nil {
		say(T("\n[6] 操作记录", "\n[6] Recorded operations"))
		say(T("    %s 执行过 unlock,备份: %s", "    unlock was run on %s; backup: %s"), st.Created, st.Termsrv.BackupPath)
		say(T("    回退命令: rdpmulti restore (管理员)", "    restore with: rdpmulti restore (as administrator)"))
	}
	say("")
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---------------------------------------------------------------- unlock

type unlockOpts struct {
	NoRestart    bool
	Force        bool
	SkipAccounts bool
}

var policyTargets = []struct{ Key, Name string }{
	{polKey, "fSingleSessionPerUser"},
	{polKey, "MaxConnectionCount"},
	{tsKey, "fSingleSessionPerUser"},
	{tsKey, "fDenyTSConnections"},
}

func runUnlock(o unlockOpts, say logger) error {
	say(T("========== 一键解锁 ==========", "========== Unlock =========="))

	if !isElevated() {
		say(T("[X] 拒绝执行:当前进程没有管理员权限", "[X] refused: this process is not elevated"))
		return fmt.Errorf(T("请以管理员身份运行(图形界面会自动弹出 UAC,命令行请右键→以管理员身份运行)", "Run as administrator (the GUI requests UAC automatically; for the CLI use \"Run as administrator\")"))
	}
	if sessionIsRDP() && !o.Force && !o.NoRestart {
		say(T("[X] 拒绝执行:当前处于 RDP 会话(%s)", "[X] refused: running inside an RDP session (%s)"), os.Getenv("SESSIONNAME"))
		return fmt.Errorf(T("检测到当前正处于 RDP 会话中:重启远程桌面服务会断开你的连接。\n请到物理控制台运行,或使用 --no-restart / --force", "An RDP session is active: restarting the Remote Desktop service would drop your connection.\nRun this at the console, or use --no-restart / --force"))
	}

	// ---- 1. 账户预检 ----
	if !o.SkipAccounts {
		say(T("\n[1/5] 检测账户密码与远程权限", "\n[1/5] Checking accounts, passwords and RDP permissions"))
		accts, acctErr := analyzeAccounts(false)
		if acctErr != nil {
			say(T("[!!] 账户枚举失败: %v", "[!!] account enumeration failed: %v"), acctErr)
			say(T("     跳过账户预检,继续执行解锁(RDP 连接时账户问题会单独提示)。", "     skipping the account pre-check and continuing (RDP will report account problems itself)."))
		} else {
			printAccounts(accts, say)
			usable, warnable := accountGate(accts, say)
			if usable == 0 && warnable == 0 {
				say(T("\n[X] 没有可用于远程登录的账户,各账户缺什么:", "\n[X] No account can log on remotely. What each account is missing:"))
				for _, a := range accts {
					if a.Disabled {
						continue
					}
					var why []string
					if !a.RemoteOK {
						why = append(why, T("未加入远程组(图形界面点[加入远程组]可修复)", "not in the RDP group (use Add to RDP group in the GUI)"))
					}
					if a.Blank != nil && *a.Blank {
						why = append(why, T("空密码,无法 RDP 登录(图形界面点[设置密码]可修复)", "blank password, cannot log on over RDP (use Set password in the GUI)"))
					}
					if a.Blank == nil {
						why = append(why, a.PwdDesc)
					}
					if len(why) == 0 {
						why = append(why, T("状态待定", "state undetermined"))
					}
					say("    - %s: %s", a.Name, strings.Join(why, ";"))
				}
				return fmt.Errorf(T("账户预检未通过(详见上方列表;图形界面可一键修复后重试,或用 --skip-accounts 跳过)", "account pre-check failed (see the list above; fix it in the GUI and retry, or pass --skip-accounts)"))
			}
			if usable > 0 {
				say(T("\n[OK] 账户预检通过(%d 个可用远程账户)。", "\n[OK] account pre-check passed (%d usable account(s))."), usable)
			} else {
				say(T("\n[!!] %d 个账户允许远程但密码状态无法自动判定(可能被临时锁定),继续执行;", "\n[!!] %d account(s) have RDP access but an undetermined password state (possibly locked out); continuing;"), warnable)
				say(T("     若之后 RDP 连接提示密码错误,请先在图形界面为该账户设置密码。", "     if RDP later reports a wrong password, set one for that account in the GUI first."))
			}
		}
	} else {
		say(T("\n[1/5] 跳过账户检测 (--skip-accounts)", "\n[1/5] account check skipped (--skip-accounts)"))
	}

	// ---- 2. 策略层 ----
	say(T("\n[2/5] 备份并放开策略层限制", "\n[2/5] Backing up and relaxing the policy layer"))
	// 二次执行时必须沿用第一次记录的"真正原始值",否则回退会把"已放开"当成原状。
	prev := loadState()
	st := stateFile{Created: time.Now().Format("2006-01-02 15:04:05"), Policy: map[string]map[string]dwordState{}}
	if prev != nil {
		say(T("     检测到历史操作记录(%s),注册表原值以首次记录为准", "     previous record found (%s); original registry values from the first run are kept"), prev.Created)
	}
	for _, t := range policyTargets {
		v, ok, _ := regGetDWORD(t.Key, t.Name)
		if st.Policy[t.Key] == nil {
			st.Policy[t.Key] = map[string]dwordState{}
		}
		if prev != nil {
			if pm, ok2 := prev.Policy[t.Key]; ok2 {
				if ds, ok3 := pm[t.Name]; ok3 {
					st.Policy[t.Key][t.Name] = ds
					continue
				}
			}
		}
		st.Policy[t.Key][t.Name] = dwordState{Existed: ok, Value: v}
	}
	newVals := map[string]map[string]uint32{
		polKey: {"fSingleSessionPerUser": 0, "MaxConnectionCount": 999999},
		tsKey:  {"fSingleSessionPerUser": 0, "fDenyTSConnections": 0},
	}
	for key, m := range newVals {
		for name, val := range m {
			if err := regSetDWORD(key, name, val); err != nil {
				say(T("[X] 注册表写入失败: %s\\%s", "[X] registry write failed: %s\\%s"), key, name)
				return fmt.Errorf(T("写入注册表失败: %v", "registry write failed: %v"), err)
			}
		}
	}
	runCmd("netsh", "advfirewall", "firewall", "set", "rule", "name=RemoteDesktop-UserMode-In-TCP", "new", "enable=yes")
	runCmd("netsh", "advfirewall", "firewall", "set", "rule", "name=RemoteDesktop-UserMode-In-UDP", "new", "enable=yes")
	say(T("[OK] fSingleSessionPerUser=0, MaxConnectionCount=999999, fDenyTSConnections=0(原值已记录)", "[OK] fSingleSessionPerUser=0, MaxConnectionCount=999999, fDenyTSConnections=0 (originals saved)"))

	// ---- 3. termsrv.dll 分析与补丁 ----
	say(T("\n[3/5] 分析 termsrv.dll", "\n[3/5] Analysing termsrv.dll"))
	orig, err := os.ReadFile(termsrvPath)
	if err != nil {
		say(T("[X] 读取 %s 失败", "[X] failed to read %s"), termsrvPath)
		return fmt.Errorf(T("读取 termsrv.dll 失败: %v", "reading termsrv.dll failed: %v"), err)
	}
	ver := dllFileVersion(termsrvPath)
	origSHA := sha256Hex(orig)
	sr := scanPatches(orig)
	patchedBytes := make([]byte, len(orig))
	copy(patchedBytes, orig)

	// 历史备份必须保留:即使本次发现文件已是解锁状态,回退仍然要用到原始文件。
	var prevTerm *termState
	if prev != nil && prev.Termsrv != nil && prev.Termsrv.BackupPath != "" {
		prevTerm = prev.Termsrv
	}

	var needSwap bool
	if sr.AlreadyPatched == len(patchDefs) {
		say(T("[OK] termsrv.dll(%s)已处于解锁状态,跳过文件补丁。", "[OK] termsrv.dll (%s) is already patched; skipping the file patch."), ver)
		st.Termsrv = prevTerm
		if prevTerm != nil {
			say(T("     保留历史备份记录: %s", "     keeping the previous backup record: %s"), prevTerm.BackupPath)
		}
		needSwap = false
	} else if len(sr.Sites) == len(patchDefs) {
		say(T("    版本: %s", "    version: %s"), ver)
		say("    SHA-256: %s", origSHA)
		for _, s := range sr.Sites {
			say("    [OK] %-24s @ %s: %s -> %s", s.Name, hexOff(s.Offset), s.OrigHex, s.NewHex)
			ob, _ := hex.DecodeString(s.OrigHex)
			nb, _ := hex.DecodeString(s.NewHex)
			copy(patchedBytes[s.Offset:s.Offset+len(ob)], nb)
		}
		st.Termsrv = &termState{
			Version: ver, OriginalSHA: origSHA,
			PatchedSHA: sha256Hex(patchedBytes), Sites: sr.Sites,
		}
		needSwap = true
	} else {
		say(T("[X] 补丁点定位失败:仅 %d/%d 处唯一命中(已解锁 %d 处)", "[X] patch sites not found: only %d/%d matched uniquely (%d already patched)"), len(sr.Sites), len(patchDefs), sr.AlreadyPatched)
		say(T("     当前 termsrv 版本 %s 可能未被本工具支持;为避免改坏系统已中止,本次未做任何更改", "     termsrv %s may not be supported; aborted before touching anything"), ver)
		return fmt.Errorf(T("补丁点定位失败(%d/%d)。当前 termsrv 版本 %s 可能未被本工具支持,已中止,系统未做任何更改", "patch sites not found (%d/%d). termsrv %s may not be supported; aborted without changes"), len(sr.Sites), len(patchDefs), ver)
	}

	if needSwap {
		// 备份
		os.MkdirAll(backupDir(), 0755)
		verSafe := strings.Map(func(r rune) rune {
			if strings.ContainsRune(`\/:*?"<>| `, r) {
				return '-'
			}
			return r
		}, ver)
		backupPath := filepath.Join(backupDir(), "termsrv-"+verSafe+".dll")
		if err := os.WriteFile(backupPath, orig, 0644); err != nil {
			return fmt.Errorf(T("写入备份失败: %v", "writing the backup failed: %v"), err)
		}
		if b, _ := os.ReadFile(backupPath); sha256Hex(b) != origSHA {
			return fmt.Errorf(T("备份校验不一致,已中止", "backup verification mismatch; aborted"))
		}
		st.Termsrv.BackupPath = backupPath
		say(T("[OK] 原始文件已备份: %s", "[OK] original file backed up: %s"), backupPath)

		// ACL 备份 + 授权
		aclPath := filepath.Join(stateDir(), "termsrv.acl")
		runCmd("icacls", termsrvPath, "/save", aclPath, "/c")
		st.Termsrv.ACLPath = aclPath
		runCmd("takeown", "/f", termsrvPath)
		if out, code := runCmd("icacls", termsrvPath, "/grant", adminSID+":F"); code != 0 {
			return fmt.Errorf(T("文件授权失败: %s", "taking ownership failed: %s"), firstLine(out))
		}

		// 写补丁后的临时文件(与目标同卷,便于原子改名)
		tmpNew := `C:\Windows\System32\termsrv.dll.rdpmulti-new`
		if err := os.WriteFile(tmpNew, patchedBytes, 0644); err != nil {
			return fmt.Errorf(T("写入补丁文件失败: %v", "writing the patched file failed: %v"), err)
		}
		st.Termsrv._tmpNew = tmpNew
		st.Termsrv._origKeep = termsrvPath + ".rdpmulti-orig"
	}

	// ---- 4/5. 停服务 → 换文件 → 起服务 ----
	say(T("\n[4/5] 应用补丁", "\n[4/5] Applying the patch"))
	pending := false
	if needSwap {
		if !o.NoRestart {
			say(T("    停止远程桌面服务(UmRdpService / TermService)...", "    stopping the Remote Desktop services (UmRdpService / TermService)..."))
			svcStop("UmRdpService", say)
			if !svcStop("TermService", say) {
				say(T("    [!!] TermService 停止失败(可能仍被会话占用),将改用重启后生效方式。", "    [!!] TermService did not stop (still in use); falling back to apply-on-reboot."))
			}
		} else {
			say(T("    --no-restart:跳过停服务,直接尝试替换文件", "    --no-restart: skipping the service stop and trying a direct file swap"))
		}
		swapped := false
		swapErr := ""
		if err := os.Rename(termsrvPath, st.Termsrv._origKeep); err == nil {
			if err := os.Rename(st.Termsrv._tmpNew, termsrvPath); err == nil {
				os.Remove(st.Termsrv._origKeep)
				swapped = true
			} else {
				swapErr = err.Error()
				os.Rename(st.Termsrv._origKeep, termsrvPath) // 回滚
			}
		} else {
			swapErr = err.Error()
		}
		if swapped {
			say(T("[OK] termsrv.dll 已替换(原始文件保留在备份目录)。", "[OK] termsrv.dll replaced (the original is kept in the backup folder)."))
		} else {
			if swapErr != "" {
				say(T("    直接替换失败: %s", "    direct swap failed: %s"), swapErr)
			}
			ok1 := schedulePendingRename(termsrvPath, st.Termsrv._origKeep)
			ok2 := schedulePendingRename(st.Termsrv._tmpNew, termsrvPath)
			if ok1 && ok2 {
				pending = true
				say(T("[!!] 文件被占用,已注册重启后替换(MoveFileEx PendingRename),需重启一次生效。", "[!!] file in use; a replace-on-reboot was scheduled (MoveFileEx) — one reboot required."))
			} else {
				say(T("[X] 无法替换文件,且无法注册重启替换 — 已中止", "[X] could not replace the file nor schedule it for reboot - aborted"))
				return fmt.Errorf(T("无法替换文件且无法计划重启替换,已中止(系统未做任何更改)", "could not replace the file nor schedule it for reboot; aborted (no changes made)"))
			}
		}
	} else {
		say(T("[OK] 文件无需更改。", "[OK] nothing to change on disk."))
	}

	if !o.NoRestart && !pending {
		say(T("    启动远程桌面服务...", "    starting the Remote Desktop services..."))
		if !svcStart("TermService") {
			say(T("[!!] TermService 启动失败,请手动执行: net start TermService", "[!!] TermService did not start; run: net start TermService"))
		}
		svcStart("UmRdpService")
	}

	// 校验:磁盘上的文件应等于补丁后的内容(防止 Defender 拦截)
	if needSwap && !pending {
		if cur, err := os.ReadFile(termsrvPath); err == nil {
			if sha256Hex(cur) != st.Termsrv.PatchedSHA {
				say(T("[!!] 警告: 磁盘上的 termsrv.dll 与补丁内容不一致,可能被 Defender 还原,", "[!!] warning: termsrv.dll on disk does not match the patched content; Defender may have restored it,"))
				say(T("     请将 C:\\Windows\\System32 加入排除或重新运行本工具。",
					"     add C:\\Windows\\System32 to the exclusions, or run this tool again."))
			} else {
				say(T("[OK] 磁盘文件校验通过(SHA-256 与补丁内容一致)。", "[OK] on-disk file verified (SHA-256 matches the patched content)."))
			}
		}
	}

	// ---- 记录状态 ----
	saveState(&st)
	say(T("[OK] 操作记录已写入 %s", "[OK] operation record written to %s"), statePath())

	say(T("\n[5/5] 完成", "\n[5/5] Done"))
	if pending {
		say(T("[!!] 请重启计算机使补丁生效。重启后再次运行体检确认。", "[!!] Reboot for the patch to take effect, then run the check again."))
	} else if o.NoRestart && needSwap {
		say(T("已补丁但未重启服务(--no-restart)。请手动执行:", "Patched but services not restarted (--no-restart). Run manually:"))
		say("    net stop UmRdpService /y && net stop TermService /y")
		say("    net start TermService && net start UmRdpService")
		say(T("或直接重启计算机。", "or simply reboot."))
	} else {
		say(T("全部完成!现在:", "All done. Next:"))
		say(T("  1. 本地保持账户 A 登录(可锁屏)", "  1. keep account A signed in locally (locking the screen is fine)"))
		say(T("  2. 另一台设备 mstsc 连接本机,用账户 B 登录", "  2. from another device, connect with mstsc and sign in as account B"))
		say(T("  3. 两个会话并行运行,互不干扰、互不踢出", "  3. both sessions run side by side without kicking each other out"))
	}
	if st.Termsrv != nil {
		say(T("\n回退方式: 图形界面点[一键回退],或命令行 rdpmulti restore", "\nTo undo: press Restore in the GUI, or run rdpmulti restore"))
		say(T("(备份: %s)", "(backup: %s)"), st.Termsrv.BackupPath)
	}
	return nil
}

// ---------------------------------------------------------------- restore

func runRestore(force bool, say logger) error {
	say(T("========== 一键回退 ==========", "========== Restore =========="))

	if !isElevated() {
		say(T("[X] 拒绝执行:当前进程没有管理员权限", "[X] refused: this process is not elevated"))
		return fmt.Errorf(T("请以管理员身份运行", "Run as administrator"))
	}
	st := loadState()
	if st == nil {
		say(T("[!!] 未找到 %s", "[!!] %s not found"), statePath())
		return fmt.Errorf(T("未找到操作记录(从未执行过 unlock,无需回退)", "no operation record found (unlock was never run, nothing to restore)"))
	}
	if sessionIsRDP() && !force {
		say(T("[X] 拒绝执行:当前处于 RDP 会话(%s)", "[X] refused: running inside an RDP session (%s)"), os.Getenv("SESSIONNAME"))
		return fmt.Errorf(T("当前处于 RDP 会话,恢复过程会重启远程桌面服务。请到物理控制台执行或加 --force", "An RDP session is active and the restore restarts the Remote Desktop service. Run it at the console or pass --force"))
	}

	// ---- 恢复 dll ----
	if st.Termsrv != nil && st.Termsrv.BackupPath != "" {
		bak, err := os.ReadFile(st.Termsrv.BackupPath)
		if err != nil {
			return fmt.Errorf(T("备份文件读取失败: %v", "reading the backup failed: %v"), err)
		}
		if sha256Hex(bak) != st.Termsrv.OriginalSHA {
			return fmt.Errorf(T("备份校验不一致,已中止(文件可能损坏)", "backup verification mismatch; aborted (the file may be corrupt)"))
		}
		runCmd("takeown", "/f", termsrvPath)
		runCmd("icacls", termsrvPath, "/grant", adminSID+":F")

		say(T("停止远程桌面服务...", "stopping the Remote Desktop services..."))
		svcStop("UmRdpService", say)
		if !svcStop("TermService", say) {
			say(T("[!!] TermService 停止失败,将改用重启后生效方式。", "[!!] TermService did not stop; falling back to apply-on-reboot."))
		}

		tmpRestore := `C:\Windows\System32\termsrv.dll.rdpmulti-restore`
		if err := os.WriteFile(tmpRestore, bak, 0644); err != nil {
			return fmt.Errorf(T("写入还原临时文件失败: %v", "writing the restore temp file failed: %v"), err)
		}
		keep := termsrvPath + ".rdpmulti-patched"
		swapped := false
		if err := os.Rename(termsrvPath, keep); err == nil {
			if err := os.Rename(tmpRestore, termsrvPath); err == nil {
				os.Remove(keep)
				swapped = true
			} else {
				os.Rename(keep, termsrvPath)
			}
		}
		if swapped {
			say(T("[OK] termsrv.dll 已还原为原始版本。", "[OK] termsrv.dll restored to the original version."))
		} else {
			ok1 := schedulePendingRename(termsrvPath, keep)
			ok2 := schedulePendingRename(tmpRestore, termsrvPath)
			if ok1 && ok2 {
				say(T("[!!] 文件被占用,已计划下次重启时自动还原(需重启一次)。", "[!!] file in use; a restore-on-reboot was scheduled — one reboot required."))
			} else {
				return fmt.Errorf(T("还原失败,系统保持当前状态", "restore failed; the system is unchanged"))
			}
		}
		if swapped {
			svcStart("TermService")
			svcStart("UmRdpService")
		}
		if st.Termsrv.ACLPath != "" {
			runCmd("icacls", `C:\Windows\System32`, "/restore", st.Termsrv.ACLPath)
		}
	} else {
		say(T("[OK] 无 termsrv.dll 补丁记录,跳过文件还原。", "[OK] no termsrv.dll record; skipping the file restore."))
	}

	// ---- 恢复注册表 ----
	say(T("还原策略注册表原值...", "restoring the original registry values..."))
	for key, m := range st.Policy {
		for name, ds := range m {
			if ds.Existed {
				regSetDWORD(key, name, ds.Value)
			} else {
				regDelValue(key, name)
			}
		}
	}
	say(T("[OK] 注册表已还原。", "[OK] registry restored."))

	// ---- 恢复"远程设置"里改过的项 ----
	if n, errs := restoreAllSettings(); n > 0 || len(errs) > 0 {
		say(T("还原远程设置项 %d 个...", "restoring %d changed setting(s)..."), n)
		for _, e := range errs {
			say("[X] %s", e)
		}
		say(T("[OK] 远程设置项已还原。", "[OK] settings restored."))
	}

	// 状态文件归档
	os.Rename(statePath(), filepath.Join(stateDir(), "state.restored-"+time.Now().Format("20060102-150405")+".json"))
	say(T("\n全部还原完成。系统已回到解锁前状态。", "\nRestore complete. The system is back to its pre-unlock state."))
	return nil
}

// ---------------------------------------------------------------- 入口

func usage() {
	fmt.Print(T(`RDP Multi —— Win11 多用户并发会话解锁工具

直接双击运行 = 图形界面(推荐,自动弹出 UAC 提权)

命令行用法(需管理员权限):
  rdpmulti check                 只读体检:账户密码 / 策略 / termsrv 补丁点分析
  rdpmulti unlock [选项]         一键解锁(自动备份,可回退)
      --no-restart               不重启远程桌面服务(补丁在下次重启服务/系统后生效)
      --force                    即使当前处于 RDP 会话也强制执行
      --skip-accounts            跳过账户密码预检
  rdpmulti restore [--force]     回退:还原 termsrv.dll 备份与注册表原值
  rdpmulti logs                  显示日志目录

原理: 放开策略层限制(注册表)+ 补丁 termsrv.dll 中 CDefPolicy 许可闸门
     (Query/Logon/Reconnect 三处,共 6 字节),使第二个账户可并行建立会话。

日志: %ProgramData%\rdpmulti\logs\rdpmulti-<时间>.log(保留最近 20 个)
`, `RDP Multi - concurrent multi-user sessions on Windows 11

Run without arguments = GUI (recommended; UAC is requested automatically)

Command line (administrator required):
  rdpmulti check                 read-only check: accounts, policy, termsrv patch sites
  rdpmulti unlock [options]      unlock in one go (backs up first, fully reversible)
      --no-restart               do not restart the Remote Desktop service
      --force                    run even inside an RDP session
      --skip-accounts            skip the password pre-check
  rdpmulti restore [--force]     restore: termsrv.dll backup and original registry values
  rdpmulti logs                  print the log directory

How it works: relax the policy layer (registry) and patch the CDefPolicy license
     gates in termsrv.dll (Query / Logon / Reconnect, 6 bytes in total) so a
     second account can hold a concurrent session.

Logs: %ProgramData%\rdpmulti\logs\rdpmulti-<time>.log (20 most recent kept)
`))
}

func main() {
	if len(os.Args) >= 2 {
		lg := NewLogger(true, true)
		defer lg.Close()
		say := lg.Say
		var err error
		switch os.Args[1] {
		case "check":
			err = runCheck(say)
		case "unlock":
			fs := flag.NewFlagSet("unlock", flag.ExitOnError)
			noRestart := fs.Bool("no-restart", false, T("不重启远程桌面服务", "do not restart the Remote Desktop service"))
			force := fs.Bool("force", false, T("RDP 会话中也强制执行", "run even inside an RDP session"))
			skipAccounts := fs.Bool("skip-accounts", false, T("跳过账户密码预检", "skip the password pre-check"))
			fs.Parse(os.Args[2:])
			err = runUnlock(unlockOpts{NoRestart: *noRestart, Force: *force, SkipAccounts: *skipAccounts}, say)
		case "restore":
			fs := flag.NewFlagSet("restore", flag.ExitOnError)
			force := fs.Bool("force", false, T("RDP 会话中也强制执行", "run even inside an RDP session"))
			fs.Parse(os.Args[2:])
			err = runRestore(*force, say)
		case "logs":
			fmt.Println(T("日志目录:", "log directory:"), filepath.Join(stateDir(), "logs"))
		case "help", "-h", "--help":
			usage()
		default:
			usage()
		}
		if err != nil {
			lg.At(lvErr, "\n[X] %v", err)
			os.Exit(1)
		}
		return
	}
	// 无参数 → 图形界面
	runGUI()
}
