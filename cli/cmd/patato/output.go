package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// printJSON 原样（缩进后）输出服务端返回的 JSON，供 Agent 解析。
func printJSON(w io.Writer, raw []byte) {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		w.Write(raw)
		fmt.Fprintln(w)
		return
	}
	buf.WriteByte('\n')
	w.Write(buf.Bytes())
}

// width 返回字符串在终端里占的列数：中日韩文字和全角符号占两列。
func width(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r < 0x1100:
		return 1
	case r <= 0x115f, // 谚文字母
		r >= 0x2e80 && r <= 0x303e, // CJK 部首、符号
		r >= 0x3041 && r <= 0x33ff, // 假名、注音、CJK 兼容
		r >= 0x3400 && r <= 0x4dbf, // CJK 扩展 A
		r >= 0x4e00 && r <= 0x9fff, // CJK 统一汉字
		r >= 0xa000 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3, // 谚文音节
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f,
		r >= 0xff00 && r <= 0xff60, // 全角
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

func pad(s string, w int) string {
	if d := w - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// table 按显示宽度对齐输出表格；right 里的列右对齐（数字）。
func table(w io.Writer, headers []string, rows [][]string, right ...int) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && width(c) > widths[i] {
				widths[i] = width(c)
			}
		}
	}
	isRight := map[int]bool{}
	for _, i := range right {
		isRight[i] = true
	}
	line := func(cells []string) {
		var b strings.Builder
		for i, c := range cells {
			if i > 0 {
				b.WriteString("  ")
			}
			if isRight[i] {
				b.WriteString(strings.Repeat(" ", max(0, widths[i]-width(c))) + c)
			} else if i == len(cells)-1 {
				b.WriteString(c)
			} else {
				b.WriteString(pad(c, widths[i]))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(headers)
	for _, r := range rows {
		line(r)
	}
}

// kv 输出「标签  值」两列，标签按显示宽度对齐。
func kv(w io.Writer, pairs [][2]string) {
	lw := 0
	for _, p := range pairs {
		lw = max(lw, width(p[0]))
	}
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		fmt.Fprintf(w, "%s  %s\n", pad(p[0], lw), p[1])
	}
}

func bytesText(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0") + " " + units[i]
}

func gbText(gb float64) string {
	s := fmt.Sprintf("%.1f", gb)
	return strings.TrimSuffix(s, ".0") + " GB"
}

// moneyText 把分换成元。
func moneyText(cents int64) string {
	if cents%100 == 0 {
		return fmt.Sprint(cents / 100)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

var periodNames = map[string]string{
	"monthly": "月", "quarterly": "季", "half_yearly": "半年", "yearly": "年", "two_yearly": "两年",
	"three_yearly": "三年", "onetime": "一次性", "reset_traffic": "重置流量",
}

var periodOrder = []string{"monthly", "quarterly", "half_yearly", "yearly", "two_yearly", "three_yearly", "onetime", "reset_traffic"}

func pricesText(p map[string]int64) string {
	if len(p) == 0 {
		return "未定价"
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		for i, o := range periodOrder {
			if o == k {
				return i
			}
		}
		return len(periodOrder)
	}
	sort.Slice(keys, func(i, j int) bool {
		return rank(keys[i]) < rank(keys[j]) || (rank(keys[i]) == rank(keys[j]) && keys[i] < keys[j])
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		name := periodNames[k]
		switch {
		case k == "onetime" || k == "reset_traffic":
			parts = append(parts, name+" "+moneyText(p[k]))
		case name != "":
			parts = append(parts, moneyText(p[k])+" / "+name)
		default:
			parts = append(parts, k+" "+moneyText(p[k]))
		}
	}
	return strings.Join(parts, "，")
}

func local(t time.Time) time.Time { return t.Local() }

func dateText(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return local(*t).Format("2006-01-02")
}

func timeText(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	lt := local(*t)
	if lt.Format("20060102") == time.Now().Format("20060102") {
		return lt.Format("15:04")
	}
	return lt.Format("01-02 15:04")
}

func agoText(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒前", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(d.Hours()/24))
}

func untilText(t time.Time) string {
	d := time.Until(t)
	switch {
	case d <= 0:
		return "已过期"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒后", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟后", int(d.Minutes())+1)
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时后", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天后", int(d.Hours()/24))
}

// valueText 把变更计划里的值换成一行文字：字符串数组用顿号连接，对象写成紧凑的 JSON。
func valueText(v any) string {
	switch t := v.(type) {
	case nil:
		return "无"
	case string:
		if t == "" {
			return "无"
		}
		return t
	case bool:
		if t {
			return "是"
		}
		return "否"
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	case []any:
		if len(t) == 0 {
			return "无"
		}
		parts := make([]string, 0, len(t))
		for _, x := range t {
			parts = append(parts, valueText(x))
		}
		return strings.Join(parts, "、")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
