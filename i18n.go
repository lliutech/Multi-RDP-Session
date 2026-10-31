//go:build windows

// i18n.go —— 界面语言
//
// 采用"调用点内联双语"(T(zh, en)),不做 key 目录:
// 词条与代码同处一行,句子结构可随语言调整,不会出现"碎片拼接"式的错误语序。
// 语言选择持久化在 %ProgramData%\rdpmulti\ui.json,首次启动按系统语言自动选择。

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type langID int

const (
	langZH langID = iota
	langEN
)

var (
	curLang   = langZH
	langMu    sync.RWMutex
	procGetUI = modKernel32.NewProc("GetUserDefaultUILanguage")
)

// T 返回当前语言下的文本。
func T(zh, en string) string {
	langMu.RLock()
	l := curLang
	langMu.RUnlock()
	if l == langEN {
		return en
	}
	return zh
}

func currentLang() langID {
	langMu.RLock()
	defer langMu.RUnlock()
	return curLang
}

func setLang(l langID) {
	langMu.Lock()
	curLang = l
	langMu.Unlock()
	saveLangPref()
}

// setLangTransient 只改内存中的语言(供 RDPMULTI_LANG 强制指定,不落盘)。
func setLangTransient(l langID) {
	langMu.Lock()
	curLang = l
	langMu.Unlock()
}

func toggleLang() {
	if currentLang() == langEN {
		setLang(langZH)
	} else {
		setLang(langEN)
	}
}

// langBadge 返回语言切换按钮上的文字(显示"将要切换到的语言")。
func langBadge() string {
	if currentLang() == langEN {
		return "中文"
	}
	return "EN"
}

// systemLang 依据系统 UI 语言决定默认语言:中文(0x04)用中文,其余用英文。
func systemLang() langID {
	r, _, _ := procGetUI.Call()
	if r == 0 {
		return langZH
	}
	if uint32(r)&0x3FF == 0x04 { // LANG_CHINESE
		return langZH
	}
	return langEN
}

type uiPref struct {
	Lang string `json:"lang"`
}

func uiPrefPath() string { return filepath.Join(stateDir(), "ui.json") }

func loadLangPref() {
	b, err := os.ReadFile(uiPrefPath())
	if err == nil {
		var p uiPref
		if json.Unmarshal(b, &p) == nil {
			switch p.Lang {
			case "en":
				setLang(langEN)
				return
			case "zh":
				setLang(langZH)
				return
			}
		}
	}
	setLang(systemLang())
}

// initLang 读取保存的语言,并允许 RDPMULTI_LANG 临时覆盖(GUI 与 CLI 都要调用,
// 否则命令行会一直停在默认语言)。
func initLang() {
	loadLangPref()
	switch os.Getenv("RDPMULTI_LANG") {
	case "en":
		setLangTransient(langEN)
	case "zh":
		setLangTransient(langZH)
	}
}

func saveLangPref() {
	if err := os.MkdirAll(stateDir(), 0755); err != nil {
		return
	}
	v := "zh"
	if currentLang() == langEN {
		v = "en"
	}
	b, _ := json.MarshalIndent(uiPref{Lang: v}, "", "  ")
	os.WriteFile(uiPrefPath(), b, 0644)
}
