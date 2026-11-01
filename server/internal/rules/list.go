package rules

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry 是规则文件里的一条规则，例如 DOMAIN-SUFFIX,google.com 或 IP-CIDR,10.0.0.0/8,no-resolve。
type Entry struct {
	Type  string
	Value string
	// Options 是值之后的参数，例如 no-resolve。
	Options []string
}

func (e Entry) String() string {
	return strings.Join(append([]string{e.Type, e.Value}, e.Options...), ",")
}

// ParseList 解析 Clash 规则文件（每行一条，# 开头为注释）。也接受 Clash 的 YAML 形式（payload: 列表）。
func ParseList(content string) []Entry {
	var out []Entry
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' || strings.HasPrefix(line, "//") || line == "payload:" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "- "); ok {
			line = strings.Trim(strings.TrimSpace(rest), `'"`)
		}
		f := strings.Split(line, ",")
		if len(f) < 2 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		e := Entry{Type: strings.ToUpper(f[0]), Value: f[1]}
		for _, o := range f[2:] {
			if o != "" {
				e.Options = append(e.Options, o)
			}
		}
		out = append(out, e)
	}
	return out
}

// 各客户端支持的规则类型；不支持的规则在下发时跳过，免得整个规则集加载失败。
var (
	clashTypes = set("DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "DOMAIN-REGEX", "GEOSITE", "GEOIP", "IP-CIDR", "IP-CIDR6",
		"IP-SUFFIX", "IP-ASN", "SRC-IP-CIDR", "SRC-PORT", "DST-PORT", "PROCESS-NAME", "PROCESS-PATH", "NETWORK")
	surgeTypes = set("DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6", "GEOIP", "IP-ASN",
		"USER-AGENT", "URL-REGEX", "PROCESS-NAME", "DEST-PORT", "SRC-IP")
)

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

// Clash 返回 rule-provider（behavior: classical）的 YAML 内容。
func Clash(entries []Entry) ([]byte, error) {
	payload := []string{}
	for _, e := range entries {
		if clashTypes[e.Type] {
			payload = append(payload, e.String())
		}
	}
	return yaml.Marshal(map[string]any{"payload": payload})
}

// Surge 返回 Surge RULE-SET 的文本内容。Clash 的 DST-PORT 在 Surge 里叫 DEST-PORT。
func Surge(entries []Entry) []byte {
	var b strings.Builder
	for _, e := range entries {
		if e.Type == "DST-PORT" {
			e.Type = "DEST-PORT"
		}
		if surgeTypes[e.Type] {
			b.WriteString(e.String())
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

// SingBox 返回 sing-box 规则集的源文件（JSON）。sing-box 的一条规则里，域名和 IP 类条件是「或」，
// 进程名、端口与它们是「且」，所以分成三条规则，规则集内的多条规则之间是「或」。
func SingBox(entries []Entry) ([]byte, error) {
	addr := map[string][]string{}
	var procs, ports []string
	for _, e := range entries {
		switch e.Type {
		case "DOMAIN":
			addr["domain"] = append(addr["domain"], e.Value)
		case "DOMAIN-SUFFIX":
			addr["domain_suffix"] = append(addr["domain_suffix"], e.Value)
		case "DOMAIN-KEYWORD":
			addr["domain_keyword"] = append(addr["domain_keyword"], e.Value)
		case "DOMAIN-REGEX":
			addr["domain_regex"] = append(addr["domain_regex"], e.Value)
		case "IP-CIDR", "IP-CIDR6":
			addr["ip_cidr"] = append(addr["ip_cidr"], e.Value)
		case "PROCESS-NAME":
			procs = append(procs, e.Value)
		case "DST-PORT":
			ports = append(ports, e.Value)
		}
	}
	rules := []map[string]any{}
	if len(addr) > 0 {
		r := map[string]any{}
		for k, v := range addr {
			r[k] = v
		}
		rules = append(rules, r)
	}
	if len(procs) > 0 {
		rules = append(rules, map[string]any{"process_name": procs})
	}
	if len(ports) > 0 {
		var ranges []string
		var single []int
		for _, p := range ports {
			if n, ok := atoiPort(p); ok {
				single = append(single, n)
			} else if strings.Contains(p, "-") {
				ranges = append(ranges, strings.Replace(p, "-", ":", 1))
			}
		}
		r := map[string]any{}
		if len(single) > 0 {
			r["port"] = single
		}
		if len(ranges) > 0 {
			r["port_range"] = ranges
		}
		if len(r) > 0 {
			rules = append(rules, r)
		}
	}
	return json.Marshal(map[string]any{"version": 2, "rules": rules})
}

func atoiPort(s string) (int, bool) {
	n := 0
	if s == "" || len(s) > 5 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0 && n <= 65535
}

// ClashSupports 和 SurgeSupports 判断客户端是否支持某种规则类型，用于下发单条规则（如 GEOIP,CN）。
func ClashSupports(typ string) bool { return clashTypes[strings.ToUpper(typ)] }

func SurgeSupports(typ string) bool { return surgeTypes[strings.ToUpper(typ)] }
