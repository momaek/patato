package rules

// Layout 是按某个用户的节点展开后的策略组和规则，各客户端格式据此生成配置。
type Layout struct {
	Groups []Group
	Rules  []Ruleset
}

// Group 是展开后的策略组。
type Group struct {
	Name      string
	Type      string
	Members   []Member
	URL       string
	Interval  int
	Tolerance int
}

// MemberKind 区分策略组成员的类型。
type MemberKind int

const (
	MemberNode MemberKind = iota
	MemberGroup
	MemberDirect
	MemberReject
)

type Member struct {
	Kind MemberKind
	Name string
}

// Build 按节点名称展开策略组：正则匹配的节点按订阅里的顺序加入，同一组里不重复。
// 一个成员都没有的组放一个 DIRECT，与 subconverter 一致，避免客户端因空组报错。
// 模板里没有兜底规则时，补一条走第一个策略组的兜底。
func Build(p *Profile, nodes []string) *Layout {
	l := &Layout{Rules: p.Rulesets}
	for _, spec := range p.Groups {
		g := Group{Name: spec.Name, Type: spec.Type, URL: spec.URL, Interval: spec.Interval, Tolerance: spec.Tolerance}
		seen := map[Member]bool{}
		add := func(m Member) {
			if !seen[m] {
				seen[m] = true
				g.Members = append(g.Members, m)
			}
		}
		for _, it := range spec.Items {
			switch {
			case it.Ref == Direct:
				add(Member{Kind: MemberDirect, Name: Direct})
			case it.Ref == Reject:
				add(Member{Kind: MemberReject, Name: Reject})
			case it.Ref != "":
				add(Member{Kind: MemberGroup, Name: it.Ref})
			default:
				for _, n := range nodes {
					if it.Pattern.MatchString(n) {
						add(Member{Kind: MemberNode, Name: n})
					}
				}
			}
		}
		if len(g.Members) == 0 {
			g.Members = []Member{{Kind: MemberDirect, Name: Direct}}
		}
		l.Groups = append(l.Groups, g)
	}
	hasFinal := false
	for _, r := range l.Rules {
		hasFinal = hasFinal || r.Final
	}
	if !hasFinal {
		l.Rules = append(append([]Ruleset{}, l.Rules...), Ruleset{Target: l.Groups[0].Name, Final: true})
	}
	return l
}

// Group 按名称查找策略组。
func (l *Layout) Group(name string) *Group {
	for i := range l.Groups {
		if l.Groups[i].Name == name {
			return &l.Groups[i]
		}
	}
	return nil
}

// Lists 返回规则引用的规则文件，按首次出现的顺序去重。
func (l *Layout) Lists() []*List { return lists(l.Rules) }
