package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseYuan(t *testing.T) {
	cases := map[string]int64{"19.9": 1990, "0.29": 29, "199.99": 19999, "10": 1000, "¥5.5": 550, "0.01": 1}
	for in, want := range cases {
		got, err := parseYuan(in)
		if err != nil || got != want {
			t.Errorf("parseYuan(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1", "9.999", "abc", "1.x"} {
		if _, err := parseYuan(bad); err == nil {
			t.Errorf("parseYuan(%q) should fail", bad)
		}
	}
	p, err := parsePrices([]string{"monthly=19.9", "yearly=199"})
	if err != nil || p["monthly"] != 1990 || p["yearly"] != 19900 {
		t.Fatalf("parsePrices: %v %v", p, err)
	}
	if _, err := parsePrices([]string{"19.9"}); err == nil {
		t.Fatal("price without period should fail")
	}
}

func TestCommandInterspersedFlags(t *testing.T) {
	c := newCommand("node update", "help")
	nm := c.fs.String("name", "", "")
	port := c.fs.Int("port", 0, "")
	if err := c.parse([]string{"tokyo-01", "--name", "东京-01", "--port=443"}); err != nil {
		t.Fatal(err)
	}
	if len(c.pos) != 1 || c.pos[0] != "tokyo-01" || *nm != "东京-01" || *port != 443 {
		t.Fatalf("pos=%v name=%q port=%d", c.pos, *nm, *port)
	}
	if !c.set["name"] || !c.set["port"] || c.set["rate"] {
		t.Fatalf("set = %v", c.set)
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"name": *nm, "port": *port, "rate": 0.0})
	if len(body) != 2 {
		t.Fatalf("only explicitly given flags are sent: %v", body)
	}
}

func TestGlobalFlags(t *testing.T) {
	a := &app{}
	rest, err := a.globalFlags([]string{"node", "--json", "show", "--profile=work", "x", "--actor", "Claude Code 会话"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.json || a.profile != "work" || a.actor != "Claude Code 会话" || strings.Join(rest, " ") != "node show x" {
		t.Fatalf("json=%v profile=%q actor=%q rest=%v", a.json, a.profile, a.actor, rest)
	}
	if _, err := a.globalFlags([]string{"--profile", "../evil"}); err == nil {
		t.Fatal("invalid profile name should fail")
	}
}

func TestTableAlignsCJK(t *testing.T) {
	var buf bytes.Buffer
	table(&buf, []string{"代号", "名称", "在线"}, [][]string{{"hk-01", "香港-01", "3"}, {"sg-01", "新加坡-01", "12"}}, 2)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %q", lines)
	}
	for _, l := range lines {
		if width(l) != width(lines[0]) {
			t.Fatalf("columns not aligned:\n%s", buf.String())
		}
	}
	if pricesText(map[string]int64{"yearly": 19900, "monthly": 1990, "reset_traffic": 500}) != "19.90 / 月，199 / 年，重置流量 5" {
		t.Fatal(pricesText(map[string]int64{"yearly": 19900, "monthly": 1990, "reset_traffic": 500}))
	}
}
