# RDP Multi

让 **两个 Windows 账户同时在线**：一个在本机使用，一个通过远程桌面登录，两边互不踢出。

Windows 客户端版本默认只允许一个交互式会话。这个限制存在于两处：几个策略值，
以及 `termsrv.dll` 里的许可闸门。本工具把这两处都放开，改之前全部备份，一键可还原。

> 实测环境：Windows 11 25H2（build 26200.9168），`termsrv.dll` 10.0.26100.9949。
> 换版本请先看 [支持的版本](#支持的版本)。

[English](README.md) · MIT 许可

---

## 功能

| | |
|---|---|
| **一键解锁 / 一键还原** | 补丁三处许可闸门（共 6 字节）+ 放开策略层；全程备份，可完整回退 |
| **只读体检** | `rdpmulti check` 只报告，不改任何东西 |
| **远程设置页** | 14 项 RDP 注册表设置（NLA、安全层、端口、重定向、超时……），逐条修改、逐条还原 |
| **账户助手** | 密码状态、远程组权限、设置密码 / 加入远程组 |
| **全量日志** | 每个决策、每次拒绝都记日志，界面可见并落盘到 `%ProgramData%\rdpmulti\logs\` |
| **中英双语** | 运行时切换，首次启动按系统语言自动选择 |

界面用 Win32 + GDI 纯手绘，无第三方依赖，单个约 3 MB 静态可执行文件。

## 前提

- Windows 10/11 客户端（见下），需要管理员权限（UAC）。
- **两个不同账户** —— 同一账户两边同登必定互顶。
- 两个账户都要有**非空密码**（RDP 不接受空密码）。
- 远程账户需在 `Remote Desktop Users` 组（管理员组成员默认可用），且已开启远程桌面。

## 快速开始

```bat
rdpmulti            rem 双击 = 图形界面(自动弹 UAC)
rdpmulti check      rem 只读体检
rdpmulti unlock     rem 补丁 + 策略(自动备份)
rdpmulti restore    rem 全部还原
rdpmulti logs       rem 打印日志目录
```

然后：本机保持账户 A 登录，另一台设备用 `mstsc` 以账户 B 连接，两个会话并行运行。

## 原理

`CDefPolicy` 是 `termsrv.dll` 里的会话许可仲裁核心（`+0x638` = 允许的会话数，
`+0x63C` = 在用会话数，客户端默认 1）。

| 方法 | 原逻辑 | 补丁 |
|---|---|---|
| `Query` | InUse == MaxAllowed 时返回 `0xD00A0013`（许可耗尽） | `jne` → `jmp`（无条件放行） |
| `Logon` | ++InUse > MaxAllowed 时拒绝新登录 | `jg` → `nop nop` |
| `Reconnect` | ++InUse > MaxAllowed 时拒绝重连 | `jg` → `nop nop` |

三处放行后，新建第二个会话不再触发"踢掉现有会话"的仲裁。

同时 `unlock` 会写入：

```
HKLM\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services
    fSingleSessionPerUser = 0
    MaxConnectionCount    = 999999
HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server
    fSingleSessionPerUser = 0
    fDenyTSConnections    = 0
```

原值只在**第一次**记录进 `state.json`；重复执行 `unlock` 不会覆盖，`restore`
因此总能回到真正的初始状态。

### 支持的版本

两级定位，**任何一处不是唯一命中就整体放弃** —— 不在猜测的基础上改系统文件：

1. **精确特征码** —— 已知版本按固定字节序列匹配；
2. **结构推断** —— 字节序列失效时改按指令形状定位：
   `mov r8d,[rdi+A] ; mov r9d,[rdi+B] ; cmp r8d,r9d ; jne`，要求字段相邻 `B == A - 4`，
   唯一命中后再用 `A/B` 锚定 `Logon` / `Reconnect`。字段偏移随 build 变化时仍可定位。

当前验证：**Windows 11 24H2 / 25H2（`termsrv` 10.0.26100 / 26200）**。

Windows 10 1809–22H2 的 `CDefPolicy` 形状不同，**未经验证** —— 没有样本来确认。
工具不会瞎试：定位不到就报告"仅 x/3 处命中"并退出。如果能提供该版本的
`termsrv.dll`，在 `patch.go` 里加一条精确特征码即可支持。

先跑 `rdpmulti check`：它会告诉你版本、SHA-256，以及三处补丁点能定位到几处。

## 远程设置

全部是 `HKLM` 下的 `REG_DWORD`。首次修改前把原值（含"该值原本不存在"）写入
`%ProgramData%\rdpmulti\settings.json`，因此每行都能单独还原，也能一键全部还原。

| 设置项 | 值 | 类型 |
|---|---|---|
| 启用远程桌面 | `fDenyTSConnections` | 开关 |
| 要求 NLA | `UserAuthentication` | 开关 |
| 安全层 | `SecurityLayer` | 枚举 |
| 最低加密级别 | `MinEncryptionLevel` | 枚举 |
| 监听端口 | `PortNumber` | 数值 |
| 保活间隔 | `KeepAliveInterval` | 数值 |
| 总是提示凭据 | `fPromptForPassword` | 开关 |
| 允许同一账户多会话 | `fSingleSessionPerUser`（策略层 + 产品层） | 开关 |
| 最大连接数 | `MaxConnectionCount` | 数值 |
| 空闲超时 | `MaxIdleTime` | 数值 |
| 禁用剪贴板 / 驱动器 / 打印机重定向 | `fDisableClip`、`fDisableCdm`、`fDisablePNR` | 开关 |

## 语言

首次启动跟随系统语言，可在标题栏用 `EN` / `中文` 切换，选择存在 `ui.json`。
临时强制：`RDPMULTI_LANG=en` 或 `zh`。

## 构建

```bash
git clone https://github.com/<you>/rdp-multi
cd rdp-multi
go build -ldflags="-s -w" -o rdpmulti.exe .
```

Go 1.21+，无外部依赖。测试会读取本机 `termsrv.dll`，没有该文件时自动跳过：

```bash
go test ./...
```

## 注意

- **补丁改的是系统 DLL。** 全部备份在 `%ProgramData%\rdpmulti\backups\`，`restore`
  可还原；但如果 `TermService` 停不下来，任何方案都不是零风险 —— 请先看日志。
- **Windows 累积更新**会换掉 `termsrv.dll` 并失效补丁；`check` 会提示，重新 `unlock` 即可。
- 若被 Defender 还原，工具会比对 SHA-256 并明确告知。接受风险可把
  `C:\Windows\System32` 加入排除。
- **探测密码每个账户消耗一次失败登录。** 默认策略（10 次失败锁 10 分钟）下反复探测
  会锁账户，所以只探测真正能用 RDP 的账户、结果缓存、已锁定的不再探测。
- 把 3389 暴露到公网会招来爆破，建议 VPN（Tailscale / ZeroTier）或端口映射 + 强密码。
- 提高会话上限可能与你的 Windows 授权条款冲突；本工具面向个人实验与单机使用，
  部署到其它场景前请自行确认合规。

## 代码结构

| 文件 | 职责 |
|---|---|
| `main.go` | 命令行、账户探测、注册表、状态文件 |
| `patch.go` | 补丁点定位（精确 + 结构） |
| `settings.go` | 设置项定义、原值存储、逐条还原 |
| `status.go` | 系统 / termsrv / 补丁 / 策略状态采集 |
| `gui.go` | 窗口、页面、命中测试、绘制、动作分发 |
| `canvas.go` | 像素画布：圆角、渐变、抗锯齿、GDI 文字 |
| `i18n.go` | `T(zh, en)` 与语言持久化 |
| `log.go` | 三路日志（内存 / 文件 / UI 回调） |

## 许可

MIT，见 [LICENSE](LICENSE)。
