package main

import (
	"regexp"
	"strings"
)

var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*\x07`)
)

// cleanSummary 清洗终端输出中的 ANSI 转义与不可打印字符，截断到 80 字（按 rune，避免中文等多字节被截断成乱码），用于列表概要。
func cleanSummary(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiOSC.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := b.String()
	// 按 rune 截断，避免多字节 UTF-8 字符被切到无效字节
	runes := []rune(out)
	if len(runes) > 80 {
		out = string(runes[:80])
	}
	return out
}
