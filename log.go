//go:build windows

// log.go —— 全量日志
//
// 三路输出:
//   1. 内存环形缓冲(UI 渲染,保留 4000 行)
//   2. 落盘文件(%ProgramData%\rdpmulti\logs\,保留最近 20 个)
//   3. 事件回调(UI 实时刷新)
//
// 所有分支都必须经 Say/At 落日志,包括各种"提前放弃"的路径。

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	lvInfo = iota
	lvOK
	lvWarn
	lvErr
	lvStep
	lvAction
)

type logEntry struct {
	At    time.Time
	Level int
	Text  string
}

type Logger struct {
	mu      sync.Mutex
	entries []logEntry
	total   int
	cap     int
	file    *os.File
	path    string
	echo    bool
	sink    func(logEntry)
	dropped int
}

// NewLogger 创建日志器。echo=true 时同时打到 stdout(命令行模式);
// toFile=true 时写入 ProgramData 日志目录(失败不影响主流程)。
func NewLogger(echo, toFile bool) *Logger {
	l := &Logger{cap: 4000, echo: echo}
	if !toFile {
		return l
	}
	dir := filepath.Join(stateDir(), "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		l.echo = echo
		return l
	}
	name := "rdpmulti-" + time.Now().Format("20060102-150405") + ".log"
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return l
	}
	l.file = f
	l.path = p
	fmt.Fprintf(f, T("# rdpmulti 会话日志 开始于 %s\n", "# rdpmulti session log started at %s\n"), time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(f, T("# 用户=%s 会话=%s 权限=%v\n", "# user=%s session=%s elevated=%v\n"), currentUserName(), orDefault(os.Getenv("SESSIONNAME"), "?"), isElevated())
	pruneLocalLogs(dir, 20)
	return l
}

func pruneLocalLogs(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "rdpmulti-") && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		os.Remove(filepath.Join(dir, n))
	}
}

// SetSink 注册实时回调(GUI 用)。回调必须是快速非阻塞的。
func (l *Logger) SetSink(fn func(logEntry)) {
	l.mu.Lock()
	l.sink = fn
	l.mu.Unlock()
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		fmt.Fprintf(l.file, T("# 会话结束 %s\n", "# session ended %s\n"), time.Now().Format("2006-01-02 15:04:05"))
		l.file.Close()
		l.file = nil
	}
}

// Path 返回当前日志文件路径(未落盘时为空)。
func (l *Logger) Path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// Say 实现 logger 类型(func(string, ...any)),按文本前缀自动定级。
func (l *Logger) Say(f string, a ...any) {
	l.At(levelOf(f), f, a...)
}

// At 以指定级别记录。多行文本会拆成多条,保证 UI 里每行颜色可控。
func (l *Logger) At(level int, f string, a ...any) {
	msg := f
	if len(a) > 0 {
		msg = fmt.Sprintf(f, a...)
	}
	for _, line := range strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n") {
		l.emit(level, line)
	}
}

func levelOf(f string) int {
	s := strings.TrimSpace(f)
	switch {
	case strings.HasPrefix(s, "[OK]"):
		return lvOK
	case strings.HasPrefix(s, "[!!]"):
		return lvWarn
	case strings.HasPrefix(s, "[X]"):
		return lvErr
	case strings.HasPrefix(s, "[>>]"), strings.HasPrefix(s, "[?]"):
		return lvAction
	}
	return lvInfo
}

func (l *Logger) emit(level int, line string) {
	e := logEntry{At: time.Now(), Level: level, Text: line}
	l.mu.Lock()
	l.entries = append(l.entries, e)
	l.total++
	if len(l.entries) > l.cap {
		l.entries = l.entries[len(l.entries)-l.cap:]
	}
	if l.file != nil {
		fmt.Fprintf(l.file, "%s %s\n", e.At.Format("15:04:05.000"), line)
	}
	sink := l.sink
	echo := l.echo
	l.mu.Unlock()

	if echo {
		fmt.Println(line)
	}
	if sink != nil {
		sink(e)
	}
}

// Snapshot 返回日志副本。
func (l *Logger) Snapshot() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]logEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// Count 返回当前内存中保留的日志条数。
func (l *Logger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Total 返回累计写入的条数(用于 UI 增量渲染的绝对游标)。
func (l *Logger) Total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// From 返回绝对序号 >= abs 的日志(已裁剪的自动跳过)。
func (l *Logger) From(abs int) []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if abs < 0 {
		abs = 0
	}
	base := l.total - len(l.entries)
	if abs < base {
		abs = base
	}
	if abs >= l.total {
		return nil
	}
	out := make([]logEntry, len(l.entries)-(abs-base))
	copy(out, l.entries[abs-base:])
	return out
}

// Clear 清空内存缓冲(磁盘文件保持不变,便于事后追溯)。
func (l *Logger) Clear() {
	l.mu.Lock()
	l.entries = nil
	l.mu.Unlock()
}

// Plain 把日志拼成纯文本(复制/导出用)。
func (l *Logger) Plain() string {
	var b strings.Builder
	for _, e := range l.Snapshot() {
		fmt.Fprintf(&b, "%s  %s\r\n", e.At.Format("15:04:05"), e.Text)
	}
	return b.String()
}

func levelTag(lv int) string {
	switch lv {
	case lvOK:
		return "OK"
	case lvWarn:
		return "WARN"
	case lvErr:
		return "ERR"
	case lvStep:
		return "STEP"
	case lvAction:
		return "ACT"
	}
	return "INFO"
}
