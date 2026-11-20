//go:build linux

package relay

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/momaek/patato/nodeapi"
)

// NFT 用 netlink 维护 table inet patato（不依赖 nft 命令）。每次 Apply 在一个 netlink 批次里
// 删掉整张表再按完整列表重建，内核原子地提交：要么全部生效，要么都不生效。已建立的连接由 conntrack 维持映射。
type NFT struct {
	mu    sync.Mutex
	netns int // 0 为当前网络命名空间；测试时指向独立的命名空间
}

// NewNFT 检查前置条件并返回 nft 实现：root、nf_tables 可用、net.ipv4.ip_forward=1。
// 不满足时返回 ErrUnavailable 包装的原因；ip_forward 没开时 patato-node 不替运营者修改 sysctl。
func NewNFT() (*NFT, error) { return newNFT(0) }

func newNFT(netns int) (*NFT, error) {
	n := &NFT{netns: netns}
	c, err := n.conn()
	if err != nil {
		return nil, fmt.Errorf("%w: 无法打开 netlink：%v", ErrUnavailable, err)
	}
	if _, err := c.ListTablesOfFamily(nftables.TableFamilyINet); err != nil {
		return nil, fmt.Errorf("%w: 内核不支持 nf_tables 或权限不足（需要 root）：%v", ErrUnavailable, err)
	}
	if netns == 0 {
		if err := CheckIPForward(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// CheckIPForward 确认内核开启了 IPv4 转发。
func CheckIPForward() error {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return fmt.Errorf("读取 net.ipv4.ip_forward 失败：%v", err)
	}
	if strings.TrimSpace(string(b)) != "1" {
		return errors.New("net.ipv4.ip_forward 没有开启，内核不会转发；请用 tolato 在机器上执行 sysctl -w net.ipv4.ip_forward=1 并写入 /etc/sysctl.d")
	}
	return nil
}

func (n *NFT) conn() (*nftables.Conn, error) {
	if n.netns != 0 {
		return nftables.New(nftables.WithNetNSFd(n.netns))
	}
	return nftables.New()
}

func (n *NFT) Name() string { return "nft" }

func (n *NFT) table() *nftables.Table {
	return &nftables.Table{Name: TableName, Family: nftables.TableFamilyINet}
}

// Apply 原子替换整张表。返回值里不在 IPv4 范围内的转发带错误，调用方据此退回用户态。
func (n *NFT) Apply(fs []nodeapi.Forward) map[int64]error {
	n.mu.Lock()
	defer n.mu.Unlock()
	rs, skipped := Build(fs)
	out := map[int64]error{}
	for _, f := range fs {
		out[f.ID] = skipped[f.ID]
	}
	c, err := n.conn()
	if err != nil {
		return fail(fs, err)
	}
	t := n.table()
	if exists, err := n.exists(c); err != nil {
		return fail(fs, err)
	} else if exists {
		c.DelTable(t)
	}
	if len(rs.Rules) == 0 {
		if err := c.Flush(); err != nil {
			return fail(fs, fmt.Errorf("清空 table inet %s 失败：%w", TableName, err))
		}
		return out
	}
	c.AddTable(t)
	chains := map[Chain]*nftables.Chain{
		ChainPre: c.AddChain(&nftables.Chain{Name: string(ChainPre), Table: t, Type: nftables.ChainTypeNAT,
			Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest}),
		ChainPost: c.AddChain(&nftables.Chain{Name: string(ChainPost), Table: t, Type: nftables.ChainTypeNAT,
			Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource}),
		ChainFwd: c.AddChain(&nftables.Chain{Name: string(ChainFwd), Table: t, Type: nftables.ChainTypeFilter,
			Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter}),
	}
	for _, name := range rs.Counters {
		c.AddObj(&nftables.CounterObj{Table: t, Name: name})
	}
	for _, r := range rs.Rules {
		c.AddRule(&nftables.Rule{Table: t, Chain: chains[r.Chain], Exprs: Exprs(r)})
	}
	if err := c.Flush(); err != nil {
		return fail(fs, fmt.Errorf("应用 table inet %s 失败：%w", TableName, err))
	}
	return out
}

func (n *NFT) exists(c *nftables.Conn) (bool, error) {
	ts, err := c.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return false, err
	}
	for _, t := range ts {
		if t.Name == TableName {
			return true, nil
		}
	}
	return false, nil
}

func fail(fs []nodeapi.Forward, err error) map[int64]error {
	out := map[int64]error{}
	for _, f := range fs {
		out[f.ID] = err
	}
	return out
}

// Read 读取所有命名计数器的字节数。
func (n *NFT) Read() (map[int64]Counters, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, err := n.conn()
	if err != nil {
		return nil, err
	}
	if ok, err := n.exists(c); err != nil || !ok {
		return map[int64]Counters{}, err
	}
	objs, err := c.GetObjects(n.table())
	if err != nil {
		return nil, err
	}
	out := map[int64]Counters{}
	for _, o := range objs {
		co, ok := o.(*nftables.CounterObj)
		if !ok {
			continue
		}
		id, dir, ok := parseCounter(co.Name)
		if !ok {
			continue
		}
		v := out[id]
		if dir == "up" {
			v.Up = co.Bytes
		} else {
			v.Down = co.Bytes
		}
		out[id] = v
	}
	return out, nil
}

// Close 不删除表：patato-node 重启期间转发继续生效，启动后按面板列表原子替换。
func (n *NFT) Close() error { return nil }

// Remove 删除整张表，供卸载时使用。
func (n *NFT) Remove() error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	if ok, err := n.exists(c); err != nil || !ok {
		return err
	}
	c.DelTable(n.table())
	return c.Flush()
}

// Exprs 把一条规则翻译成 netlink 表达式。寄存器用 1（NFT_REG_1）和 2。
func Exprs(r Rule) []expr.Any {
	var e []expr.Any
	if r.IPv4 {
		e = append(e,
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		)
	}
	if r.Proto != "" {
		proto := byte(unix.IPPROTO_TCP)
		if r.Proto == "udp" {
			proto = unix.IPPROTO_UDP
		}
		e = append(e,
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(r.DPort))},
		)
	}
	if r.CtMark != 0 {
		e = append(e,
			&expr.Ct{Key: expr.CtKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(r.CtMark)},
		)
	}
	if r.Direction != "" {
		// IP_CT_DIR_ORIGINAL = 0，IP_CT_DIR_REPLY = 1（linux/netfilter/nf_conntrack_tuple_common.h）
		dir := byte(0)
		if r.Direction == "reply" {
			dir = 1
		}
		e = append(e,
			&expr.Ct{Key: expr.CtKeyDIRECTION, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{dir}},
		)
	}
	if r.SetMark != 0 {
		e = append(e,
			&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(r.SetMark)},
			&expr.Ct{Key: expr.CtKeyMARK, Register: 1, SourceRegister: true},
		)
	}
	if r.DNAT.IsValid() {
		ip := r.DNAT.Addr().As4()
		e = append(e,
			&expr.Immediate{Register: 1, Data: ip[:]},
			&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(r.DNAT.Port())},
			&expr.NAT{Type: expr.NATTypeDestNAT, Family: unix.NFPROTO_IPV4, RegAddrMin: 1, RegProtoMin: 2},
		)
	}
	if r.Masquerade {
		e = append(e, &expr.Masq{})
	}
	if r.Counter != "" {
		e = append(e, &expr.Objref{Type: unix.NFT_OBJECT_COUNTER, Name: r.Counter})
	}
	return e
}
