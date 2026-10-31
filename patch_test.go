//go:build windows

// patch_test.go —— 用真实的 termsrv.dll 校验"结构推断"与"精确特征码"结论一致。
//
// 优先取 backups 目录里的原始文件(未打补丁),这样即使本机已解锁也能测;
// 没有该文件的环境会自动跳过。

package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// originalTermsrv 返回一份"未打补丁"的 termsrv 内容。
func originalTermsrv(t *testing.T) []byte {
	t.Helper()
	ents, err := os.ReadDir(backupDir())
	if err == nil {
		for _, e := range ents {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "termsrv-") {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(backupDir(), e.Name())); err == nil {
				return b
			}
		}
	}
	b, err := os.ReadFile(termsrvPath)
	if err != nil {
		t.Skipf("读取 %s 失败,跳过: %v", termsrvPath, err)
	}
	return b
}

func TestStructuralMatchesExact(t *testing.T) {
	data := originalTermsrv(t)
	ex, ok := scanExact(data)
	if !ok {
		t.Skipf("精确扫描未全部命中,本用例不适用: sites=%d patched=%d missing=%d",
			len(ex.Sites), ex.AlreadyPatched, ex.Missing)
	}
	st, ok2 := scanStructural(data)
	if !ok2 {
		t.Fatalf("结构推断未能完成: sites=%d patched=%d missing=%d", len(st.Sites), st.AlreadyPatched, st.Missing)
	}
	if len(st.Sites) != len(ex.Sites) {
		t.Fatalf("站点数量不一致: 结构=%d 精确=%d", len(st.Sites), len(ex.Sites))
	}
	for i := range ex.Sites {
		if ex.Sites[i].Offset != st.Sites[i].Offset {
			t.Errorf("%s 偏移不一致: 结构=%s 精确=%s",
				ex.Sites[i].Name, hexOff(st.Sites[i].Offset), hexOff(ex.Sites[i].Offset))
		}
	}
	t.Logf("结构推断与精确匹配一致,共 %d 处", len(st.Sites))
}

// 模拟"字段偏移变化"的版本差异,验证结构推断仍能定位。
func TestStructuralShiftedLayout(t *testing.T) {
	data := originalTermsrv(t)
	ex, ok := scanExact(data)
	if !ok || len(ex.Sites) != 3 {
		t.Skip("需要未打补丁的原始文件")
	}
	// 把 0x638/0x63C 整体搬到 0x718/0x71C(模拟另一版本的 CDefPolicy 布局)
	shifted := make([]byte, len(data))
	copy(shifted, data)
	for i := 0; i+4 <= len(shifted); i++ {
		if shifted[i] == 0x38 && shifted[i+1] == 0x06 && shifted[i+2] == 0x00 && shifted[i+3] == 0x00 {
			shifted[i], shifted[i+1] = 0x18, 0x07
		}
		if shifted[i] == 0x3C && shifted[i+1] == 0x06 && shifted[i+2] == 0x00 && shifted[i+3] == 0x00 {
			shifted[i], shifted[i+1] = 0x1C, 0x07
		}
	}
	if _, okExact := scanExact(shifted); okExact {
		t.Skip("位移后精确特征码仍命中,用例不成立")
	}
	st, ok2 := scanStructural(shifted)
	if !ok2 {
		t.Fatalf("位移布局下结构推断失败: sites=%d patched=%d missing=%d",
			len(st.Sites), st.AlreadyPatched, st.Missing)
	}
	t.Logf("位移布局下仍定位到 %d 处补丁点", len(st.Sites))
}

func TestPatchedDetection(t *testing.T) {
	data := originalTermsrv(t)
	sr, ok := scanExact(data)
	if !ok || len(sr.Sites) != 3 {
		t.Skip("当前文件不是未打补丁状态,跳过")
	}
	patched := make([]byte, len(data))
	copy(patched, data)
	for _, s := range sr.Sites {
		orig, err := hex.DecodeString(s.OrigHex)
		if err != nil {
			t.Fatalf("解析原字节失败: %v", err)
		}
		nb, err := hex.DecodeString(s.NewHex)
		if err != nil {
			t.Fatalf("解析新字节失败: %v", err)
		}
		copy(patched[s.Offset:s.Offset+len(orig)], nb)
	}
	after, ok2 := scanExact(patched)
	if !ok2 || after.AlreadyPatched != 3 {
		t.Fatalf("精确扫描未识别出已打补丁状态: %+v", after)
	}
	afterS, ok3 := scanStructural(patched)
	if !ok3 || afterS.AlreadyPatched != 3 {
		t.Fatalf("结构扫描未识别出已打补丁状态: %+v", afterS)
	}
}
