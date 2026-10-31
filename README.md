# RDP Multi

Keep **two Windows accounts signed in at the same time**: one locally, one over
Remote Desktop — without either session kicking the other off.

Windows client editions normally allow only one interactive session. The limit is
enforced in two places: a few policy values, and a licence gate inside
`termsrv.dll`. This tool relaxes both, backs up everything first, and can put it
all back with one click.

> Verified on Windows 11 25H2 (build 26200.9168), `termsrv.dll` 10.0.26100.9949.
> See [Supported versions](#supported-versions) before you try another build.

[中文说明](README.zh-CN.md) · MIT licensed

---

## Features

| | |
|---|---|
| **One-click unlock / restore** | Patches three licence gates (6 bytes) and relaxes the policy layer. Full backup, one-click revert. |
| **Read-only check** | `rdpmulti check` reports accounts, policy and patch sites without changing anything. |
| **Remote settings page** | 14 RDP registry settings (NLA, security layer, port, redirection, timeouts…) editable one by one, each with its own saved original value and a per-item *Restore*. |
| **Account helper** | Password state, RDP group membership, set-password / add-to-group actions. |
| **Full log** | Every decision and every refusal is logged, in the UI and to `%ProgramData%\rdpmulti\logs\`. |
| **Bilingual UI** | 中文 / English, switchable at runtime, chosen from the system language on first run. |

The GUI is hand-drawn with Win32 + GDI only — no third-party dependencies, one
~3 MB static binary.

## Requirements

- Windows 10/11 client (see below), administrator rights (UAC).
- **Two different accounts** — the same account on both ends will always push the
  other out.
- Both accounts need a **non-empty password** (RDP refuses blank passwords).
- The remote account must be in `Remote Desktop Users` (members of `Administrators`
  already are), and Remote Desktop must be enabled.

## Quick start

```bat
rdpmulti            rem double-click: GUI (asks for UAC)
rdpmulti check      rem read-only check
rdpmulti unlock     rem patch + policy, with backup
rdpmulti restore    rem undo everything
rdpmulti logs       rem print the log directory
```

Then: stay signed in as account A locally, connect from another machine with
`mstsc` as account B, and both sessions run side by side.

## How it works

`CDefPolicy` is the session-licence arbiter inside `termsrv.dll`
(`+0x638` = allowed sessions, `+0x63C` = sessions in use, client default 1).

| Method | Original behaviour | Patch |
|---|---|---|
| `Query` | returns `0xD00A0013` ("licence exhausted") when InUse == MaxAllowed | `jne` → `jmp` (always allow) |
| `Logon` | refuses a new logon when ++InUse > MaxAllowed | `jg` → `nop nop` |
| `Reconnect` | refuses a reconnect when ++InUse > MaxAllowed | `jg` → `nop nop` |

With all three gates open, creating a second session no longer triggers the
"kick the existing session" arbitration.

Alongside that, `unlock` sets:

```
HKLM\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services
    fSingleSessionPerUser = 0
    MaxConnectionCount    = 999999
HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server
    fSingleSessionPerUser = 0
    fDenyTSConnections    = 0
```

Original values are recorded in `state.json` **the first time**; re-running
`unlock` never overwrites them, so `restore` always returns to the true original.

### Supported versions

Two-stage location, and **any site that is not uniquely matched aborts the whole
operation** — the tool refuses to patch on a guess:

1. **Exact signature** — for builds whose bytes are already known.
2. **Structural match** — when the byte sequence changed, locate by instruction
   shape instead: `mov r8d,[rdi+A] ; mov r9d,[rdi+B] ; cmp r8d,r9d ; jne` with the
   field-adjacency constraint `B == A - 4`, then reuse `A/B` to anchor `Logon` and
   `Reconnect`. This survives field-offset changes between builds.

Currently verified: **Windows 11 24H2 / 25H2 (`termsrv` 10.0.26100 / 26200)**.

Windows 10 1809–22H2 has a different `CDefPolicy` shape and is **not** verified —
no sample binary was available. Rather than guess, the tool reports how many sites
it could locate and exits. If you can provide that `termsrv.dll`, adding an exact
signature is a few lines in `patch.go`.

Run `rdpmulti check` first: it tells you your version, the SHA-256, and how many of
the three sites it can locate.

## Remote settings

All items are `REG_DWORD` values under `HKLM`. The original value (including
"this value did not exist") is saved to `%ProgramData%\rdpmulti\settings.json`
before the first change, so each row can be restored on its own, or all at once.

| Setting | Value | Type |
|---|---|---|
| Enable Remote Desktop | `fDenyTSConnections` | switch |
| Require NLA | `UserAuthentication` | switch |
| Security layer | `SecurityLayer` | enum |
| Minimum encryption level | `MinEncryptionLevel` | enum |
| Listening port | `PortNumber` | number |
| Keep-alive interval | `KeepAliveInterval` | number |
| Always prompt for credentials | `fPromptForPassword` | switch |
| Allow multiple sessions per user | `fSingleSessionPerUser` (policy + product) | switch |
| Max connections | `MaxConnectionCount` | number |
| Idle timeout | `MaxIdleTime` | number |
| Disable clipboard / drive / printer redirection | `fDisableClip`, `fDisableCdm`, `fDisablePNR` | switch |

## Language

The UI follows the system language on first run and can be switched from the
header (`EN` / `中文`). The choice is stored in `ui.json`.
Set `RDPMULTI_LANG=en` or `zh` to force a language for one run.

## Build

```bash
git clone https://github.com/<you>/rdp-multi
cd rdp-multi
go build -ldflags="-s -w" -o rdpmulti.exe .
```

Go 1.21+, no external modules. Tests use the local `termsrv.dll` and back off
gracefully when it is absent:

```bash
go test ./...
```

## Safety notes

- **The patch modifies a system DLL.** Everything is backed up to
  `%ProgramData%\rdpmulti\backups\` and `restore` reverts it, but a failed run
  while `TermService` cannot be stopped is not risk-free. Read the log.
- A **Windows cumulative update** replaces `termsrv.dll` and undoes the patch.
  `check` will say so; run `unlock` again.
- If Defender restores the file, the tool detects the SHA-256 mismatch and tells you.
  Add `C:\Windows\System32` to exclusions if you accept that trade-off.
- **Password probing costs one failed logon** per account with a password.
  With the default policy (10 failures / 10 minutes) repeated probing can lock an
  account, so probing only covers accounts that can actually use RDP, results are
  cached, and locked-out accounts are never probed again.
- Exposing 3389 to the internet invites brute force — use a VPN (Tailscale,
  ZeroTier) or port forwarding with strong passwords.
- Raising the session limit may conflict with your Windows licence terms; this
  tool is intended for personal lab and single-machine use. Check what applies
  to you before deploying it anywhere else.

## Layout

| File | Purpose |
|---|---|
| `main.go` | CLI, account probing, registry, state file |
| `patch.go` | Patch-site location (exact + structural) |
| `settings.go` | Remote settings definitions, original-value store, per-item restore |
| `status.go` | OS / termsrv / patch / policy status collection |
| `gui.go` | Window, pages, hit-testing, painting, actions |
| `canvas.go` | Pixel canvas: rounded rects, gradients, AA, GDI text |
| `i18n.go` | `T(zh, en)` and language persistence |
| `log.go` | Three-way log (memory / file / UI callback) |

## License

MIT — see [LICENSE](LICENSE).
