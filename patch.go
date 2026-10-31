//go:build windows

// patch.go —— termsrv.dll 补丁点定位
//
// 两级策略,任何一处不是"唯一命中"就整体放弃(宁可报错,不做盲改):
//   1) 精确特征码:已验证版本按固定字节序列匹配;
//   2) 结构推断:字节序列因版本差异失效时,改用指令形状定位 ——
//      mov r8d,[rdi+A] ; mov r9d,[rdi+B] ; cmp r8d,r9d ; jne,且 B == A-4,
//      唯一命中后用解出的 A/B 锚定 Logon / Reconnect。
//      这样字段偏移随 build 变化时仍可定位。

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// 定位方式
const (
	methodNone = iota
	methodExact
	methodStructural
)

func methodLabel(m int) string {
	switch m {
	case methodExact:
		return T("精确", "exact")
	case methodStructural:
		return T("结构", "structural")
	}
	return T("无", "none")
}

type patchDef struct {
	Name     string
	DescZh   string
	DescEn   string
	Match    []byte // 特征码(含跳转操作码,需在文件中唯一)
	PatchOff int    // Match 内被替换字节的起始下标
	New      []byte // 替换后的固定字节
	KeepDisp bool   // true: 替换第二字节保留原跳转位移(短跳转条件→无条件)
}

func (d patchDef) desc() string {
	if curLang == langEN {
		return d.DescEn
	}
	return d.DescZh
}

var patchDefs = []patchDef{
	{
		Name:     "CDefPolicy::Query",
		DescZh:   "InUse==MaxAllowed 时返回\"许可耗尽\" → 改为无条件放行",
		DescEn:   "returns \"licence exhausted\" when InUse==MaxAllowed -> always allow",
		Match:    []byte{0x44, 0x8B, 0x87, 0x3C, 0x06, 0x00, 0x00, 0x44, 0x8B, 0x8F, 0x38, 0x06, 0x00, 0x00, 0x45, 0x3B, 0xC1, 0x75},
		PatchOff: 17,
		New:      []byte{0xEB},
		KeepDisp: true,
	},
	{
		Name:     "CDefPolicy::Logon",
		DescZh:   "新登录 InUse+1>MaxAllowed 时拒绝 → 改为永不拒绝",
		DescEn:   "rejects a new logon when InUse+1>MaxAllowed -> never reject",
		Match:    []byte{0x3B, 0x9F, 0x38, 0x06, 0x00, 0x00, 0x7F},
		PatchOff: 6,
		New:      []byte{0x90, 0x90},
	},
	{
		Name:     "CDefPolicy::Reconnect",
		DescZh:   "重连 InUse+1>MaxAllowed 时拒绝 → 改为永不拒绝",
		DescEn:   "rejects a reconnect when InUse+1>MaxAllowed -> never reject",
		Match:    []byte{0x3B, 0x87, 0x38, 0x06, 0x00, 0x00, 0x7F},
		PatchOff: 6,
		New:      []byte{0x90, 0x90},
	},
}

type patchSite struct {
	Name    string `json:"name"`
	Offset  int    `json:"offset"`
	OrigHex string `json:"orig"`
	NewHex  string `json:"new"`
	Desc    string `json:"desc"`
	Method  string `json:"method,omitempty"`
}

type scanResult struct {
	Sites          []patchSite
	AlreadyPatched int
	Missing        int
	Method         int // methodExact / methodStructural
}

func (r scanResult) Complete() bool {
	return len(r.Sites)+r.AlreadyPatched == len(patchDefs) && r.Missing == 0
}

func hexOff(off int) string { return fmt.Sprintf("0x%X", off) }

// scanPatches 依次尝试精确匹配与结构推断。
func scanPatches(data []byte) scanResult {
	if r, ok := scanExact(data); ok {
		return r
	}
	if r, ok := scanStructural(data); ok {
		return r
	}
	// 都失败时返回精确扫描的结果(用于报告"命中几处")
	r, _ := scanExact(data)
	return r
}

// ---------------------------------------------------------------- 精确匹配

func scanExact(data []byte) (scanResult, bool) {
	var res scanResult
	res.Method = methodExact
	for _, d := range patchDefs {
		fixed := d.Match[:d.PatchOff] // 跳转码之前的固定字节
		var hits, patched []int
		i := bytes.Index(data, fixed)
		for i != -1 {
			switch {
			case data[i+d.PatchOff] == d.Match[d.PatchOff]:
				hits = append(hits, i)
			case data[i+d.PatchOff] == d.New[0]:
				if d.KeepDisp || (i+d.PatchOff+1 < len(data) && data[i+d.PatchOff+1] == d.New[1]) {
					patched = append(patched, i)
				}
			}
			j := bytes.Index(data[i+1:], fixed)
			if j == -1 {
				break
			}
			i = i + 1 + j
		}
		switch {
		case len(hits) == 1:
			off := hits[0]
			orig := data[off+d.PatchOff : off+d.PatchOff+2]
			var nb []byte
			if d.KeepDisp {
				nb = []byte{d.New[0], orig[1]}
			} else {
				nb = d.New
			}
			res.Sites = append(res.Sites, patchSite{
				Name: d.Name, Offset: off + d.PatchOff,
				OrigHex: hex.EncodeToString(orig), NewHex: hex.EncodeToString(nb),
				Desc: d.desc(), Method: "exact",
			})
		case len(hits) > 1:
			res.Missing++ // 多处命中视为不安全,按缺失处理
		case len(patched) == 1:
			res.AlreadyPatched++
		default:
			res.Missing++
		}
	}
	return res, res.Complete()
}

// ---------------------------------------------------------------- 结构推断

func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func scanStructural(data []byte) (scanResult, bool) {
	var res scanResult
	res.Method = methodStructural

	// ---- 1) Query: mov r8d,[rdi+A] / mov r9d,[rdi+B] / cmp r8d,r9d / jne,且 B = A-4
	type qHit struct {
		off     int
		maxOff  uint32
		patched bool
	}
	var qs []qHit
	for i := 0; i+19 <= len(data); i++ {
		if data[i] != 0x44 || data[i+1] != 0x8B || data[i+2] != 0x87 {
			continue
		}
		if data[i+7] != 0x44 || data[i+8] != 0x8B || data[i+9] != 0x8F {
			continue
		}
		inUse := le32(data[i+3:])
		maxAllowed := le32(data[i+10:])
		if maxAllowed+4 != inUse { // 两个字段相邻:+638(上限) 与 +63C(在用)
			continue
		}
		if data[i+14] != 0x45 || data[i+15] != 0x3B || data[i+16] != 0xC1 {
			continue
		}
		switch data[i+17] {
		case 0x75:
			qs = append(qs, qHit{i, maxAllowed, false})
		case 0xEB:
			qs = append(qs, qHit{i, maxAllowed, true})
		}
	}
	if len(qs) != 1 {
		res.Missing = len(patchDefs)
		return res, false
	}
	q := qs[0]
	if q.patched {
		res.AlreadyPatched++
	} else {
		res.Sites = append(res.Sites, patchSite{
			Name: patchDefs[0].Name, Offset: q.off + 17,
			OrigHex: hex.EncodeToString(data[q.off+17 : q.off+19]),
			NewHex:  hex.EncodeToString([]byte{0xEB, data[q.off+18]}),
			Desc:    patchDefs[0].desc(), Method: "structural",
		})
	}

	// ---- 2) Logon / Reconnect: cmp <reg>,[rdi+maxOff] + jg
	disp := []byte{byte(q.maxOff), byte(q.maxOff >> 8), byte(q.maxOff >> 16), byte(q.maxOff >> 24)}
	find := func(op0, op1 byte) (unpatchedOff int, patchedOff int) {
		prefix := append([]byte{op0, op1}, disp...)
		unpatchedOff, patchedOff = -1, -1
		i := bytes.Index(data, prefix)
		for i != -1 {
			if i+7 <= len(data) {
				switch {
				case data[i+6] == 0x7F:
					if unpatchedOff >= 0 { // 多处命中 → 不安全
						unpatchedOff = -2
					} else if unpatchedOff != -2 {
						unpatchedOff = i
					}
				case data[i+6] == 0x90 && i+7 <= len(data) && data[i+7] == 0x90:
					if patchedOff >= 0 {
						patchedOff = -2
					} else if patchedOff != -2 {
						patchedOff = i
					}
				}
			}
			j := bytes.Index(data[i+1:], prefix)
			if j == -1 {
				break
			}
			i = i + 1 + j
		}
		return
	}

	type slot struct {
		def patchDef
		op0 byte
		op1 byte
	}
	slots := []slot{
		{patchDefs[1], 0x3B, 0x9F},
		{patchDefs[2], 0x3B, 0x87},
	}
	for _, sl := range slots {
		u, p := find(sl.op0, sl.op1)
		switch {
		case u == -2 || (u >= 0 && p >= 0):
			res.Missing++
		case u >= 0:
			res.Sites = append(res.Sites, patchSite{
				Name: sl.def.Name, Offset: u + 6,
				OrigHex: hex.EncodeToString(data[u+6 : u+8]),
				NewHex:  "9090",
				Desc:    sl.def.desc(), Method: "structural",
			})
		case p >= 0:
			res.AlreadyPatched++
		default:
			res.Missing++
		}
	}
	return res, res.Complete()
}
