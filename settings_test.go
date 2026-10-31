//go:build windows

// settings_test.go —— 校验"远程设置"的原值记录与还原判定(不碰真实注册表)。
// 通过改写 ProgramData 把 settings.json 落到临时目录。

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	// 初始为空
	s := loadSettingsStore()
	if len(s.Items) != 0 {
		t.Fatalf("期望空仓库,实际 %d 项", len(s.Items))
	}

	// 记录一条"原本不存在"的原值
	id := settingID(polKey, "fDisableClip")
	s.Items[id] = dwordState{Existed: false, Value: 0}
	if err := saveSettingsStore(s); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	loaded := loadSettingsStore()
	ds, ok := loaded.Items[id]
	if !ok {
		t.Fatal("重新加载后丢失记录")
	}
	if ds.Existed {
		t.Error("Existed 应为 false")
	}

	// originOf 在没有 state.json 时应回落到 settings.json
	got, ok := originOf(polKey, "fDisableClip")
	if !ok {
		t.Fatal("originOf 未取到 settings.json 的记录")
	}
	if got.Existed {
		t.Error("originOf 返回的 Existed 应为 false")
	}

	// 文件确实落在临时目录里,没有污染真实 ProgramData
	if _, err := os.Stat(filepath.Join(dir, "rdpmulti", "settings.json")); err != nil {
		t.Fatalf("settings.json 未生成: %v", err)
	}
}

func TestSettingValueHelpers(t *testing.T) {
	def := settingDef{Kind: skBoolInv} // 0 = 开
	if !boolOn(def, 0) {
		t.Error("skBoolInv: 0 应为开")
	}
	if boolOn(def, 1) {
		t.Error("skBoolInv: 1 应为关")
	}
	if v := boolValue(def, true); v != 0 {
		t.Errorf("skBoolInv 开应写 0,实际 %d", v)
	}

	normal := settingDef{Kind: skBool}
	if !boolOn(normal, 1) || boolOn(normal, 0) {
		t.Error("skBool: 1 开 / 0 关")
	}
	if v := boolValue(normal, true); v != 1 {
		t.Errorf("skBool 开应写 1,实际 %d", v)
	}

	enum := settingDef{Kind: skEnum, Enum: []enumOpt{{0, "RDP 安全层", "RDP security"}, {1, "协商", "Negotiate"}, {2, "SSL/TLS", "SSL/TLS"}}}
	if enumLabel(enum, 2) != "SSL/TLS" {
		t.Errorf("枚举标签错误: %s", enumLabel(enum, 2))
	}
	if enumLabel(enum, 9) != "9" {
		t.Errorf("未知枚举值应回退为数字,实际 %s", enumLabel(enum, 9))
	}
}
