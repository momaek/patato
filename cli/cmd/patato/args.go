package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

var errUsage = errors.New("usage")

// command 包一层 FlagSet：允许 flag 写在位置参数后面（patato node show tokyo-01 --json），
// 并记录哪些 flag 被显式设置过，写接口只发送用户给出的字段。
type command struct {
	fs   *flag.FlagSet
	pos  []string
	set  map[string]bool
	help string
}

func newCommand(name, help string) *command {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return &command{fs: fs, help: help}
}

func (c *command) parse(args []string) error {
	for {
		if err := c.fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				c.usage(os.Stdout)
				return flag.ErrHelp
			}
			return fmt.Errorf("%v\n\n%s", err, c.usageText())
		}
		rest := c.fs.Args()
		if len(rest) == 0 {
			break
		}
		if rest[0] == "--" {
			c.pos = append(c.pos, rest[1:]...)
			break
		}
		c.pos = append(c.pos, rest[0])
		args = rest[1:]
	}
	c.set = map[string]bool{}
	c.fs.Visit(func(f *flag.Flag) { c.set[f.Name] = true })
	return nil
}

func (c *command) usageText() string {
	var b strings.Builder
	b.WriteString(c.help)
	b.WriteString("\n")
	first := true
	c.fs.VisitAll(func(f *flag.Flag) {
		if first {
			b.WriteString("\n选项：\n")
			first = false
		}
		name := "--" + f.Name
		if _, ok := f.Value.(boolFlag); ok {
			// 布尔 flag 不写取值
		} else {
			name += " <值>"
		}
		def := ""
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "[]" {
			def = "（默认 " + f.DefValue + "）"
		}
		fmt.Fprintf(&b, "  %-24s %s%s\n", name, f.Usage, def)
	})
	return b.String()
}

func (c *command) usage(w io.Writer) { fmt.Fprint(w, c.usageText()) }

type boolFlag interface{ IsBoolFlag() bool }

// arg 取第 i 个位置参数，缺少时给出这条命令的用法。
func (c *command) arg(i int, what string) (string, error) {
	if i >= len(c.pos) || strings.TrimSpace(c.pos[i]) == "" {
		return "", fmt.Errorf("缺少%s\n\n%s", what, c.usageText())
	}
	return c.pos[i], nil
}

func (c *command) noExtra(n int) error {
	if len(c.pos) > n {
		return fmt.Errorf("多余的参数 %q\n\n%s", strings.Join(c.pos[n:], " "), c.usageText())
	}
	return nil
}

// multi 是可以重复给出的 flag，例如 --plan premium --plan standard，也接受逗号分隔。
type multi []string

func (m *multi) String() string { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

// writeFlags 是所有写命令共有的选项。
type writeFlags struct {
	dryRun      bool
	wait        bool
	timeout     time.Duration
	instruction string
}

func (c *command) writeFlags() *writeFlags {
	w := &writeFlags{}
	c.fs.BoolVar(&w.dryRun, "dry-run", false, "只预演，返回变更计划和 change_id，不执行")
	c.fs.BoolVar(&w.wait, "wait", false, "需要审批时等待结果")
	c.fs.DurationVar(&w.timeout, "timeout", 30*time.Minute, "--wait 最多等多久")
	c.fs.StringVar(&w.instruction, "instruction", os.Getenv("PATATO_INSTRUCTION"), "用户的原话，显示在审批卡片上（也可以用 $PATATO_INSTRUCTION）")
	return w
}

// parseYuan 把「19.9」这样的元换成分，避免浮点误差：按字符串处理小数部分。
func parseYuan(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "¥"), "￥"))
	if s == "" {
		return 0, errors.New("价格为空")
	}
	neg := strings.HasPrefix(s, "-")
	if neg {
		return 0, errors.New("价格不能为负数")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 {
		return 0, fmt.Errorf("价格 %s 最多两位小数", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("价格 %q 不是数字", s)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("价格 %q 不是数字", s)
	}
	return w*100 + f, nil
}

// parsePrices 解析 --price monthly=19.9，返回以分为单位的价格。
func parsePrices(items []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("--price %q 应写成 周期=元，例如 monthly=19.9", it)
		}
		cents, err := parseYuan(v)
		if err != nil {
			return nil, err
		}
		out[k] = cents
	}
	return out, nil
}

// jsonArg 读取 JSON 参数：直接写 JSON，或 @文件路径。
func jsonArg(v string) (any, error) {
	b := []byte(v)
	if strings.HasPrefix(v, "@") {
		var err error
		if b, err = os.ReadFile(v[1:]); err != nil {
			return nil, err
		}
	}
	var out any
	if err := jsonUnmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("不是合法 JSON：%v", err)
	}
	return out, nil
}
