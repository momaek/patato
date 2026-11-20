// Package nodeapi 定义 Patato 原生节点协议的报文，服务端与 patato-node 共用。
//
// 协议只有四个接口，都用机器 token（Authorization: Bearer）鉴权：
//
//	POST /api/v1/node/enroll   凭一次性注册码换取机器 token（不需要 token）
//	POST /api/v1/node/sync     上报本地已有的版本号，取回有变化的节点配置与用户
//	POST /api/v1/node/report   上报一批流量、在线 IP 和运行状态，带序号，面板确认后节点才丢弃
//	GET  /api/v1/node/ws       WebSocket，面板在配置或用户变化时推送 changed，节点随即调用 sync
//
// 与 UniProxy 不同，这里的节点配置是 Patato 自己的模型，不沿用 Xboard 的 protocol_settings。
package nodeapi

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

const (
	PathEnroll = "/api/v1/node/enroll"
	PathSync   = "/api/v1/node/sync"
	PathReport = "/api/v1/node/report"
	PathWS     = "/api/v1/node/ws"
)

// EnrollRequest 凭注册码或 tolato 的机器身份证明换取机器 token。
// 注册码由 patato machine install-cmd 生成，一小时内有效，只能用一次；
// Attestation 是本机 tolato-agent 签发的一次性证明（tolato-agent --attest <面板地址>），二者给一个。
type EnrollRequest struct {
	Code        string `json:"code,omitempty"`
	Attestation string `json:"attestation,omitempty"`
	Hostname    string `json:"hostname"`
	Version     string `json:"version"`
}

type EnrollResponse struct {
	Machine string `json:"machine"`
	Token   string `json:"token"`
}

// SyncRequest 带上节点本地已有的版本号；面板只返回有变化的部分。
type SyncRequest struct {
	Version string              `json:"version"` // patato-node 版本
	Kernel  string              `json:"kernel"`  // 内核及版本，例如 sing-box 1.13.21
	Nodes   map[int64]KnownNode `json:"nodes"`
	// ForwardsRev 是本地已应用的转发列表版本（ForwardSync.Rev），没有转发时为空。
	ForwardsRev string `json:"forwards_rev,omitempty"`
}

// KnownNode 是节点本地已应用的版本。
type KnownNode struct {
	ConfigRev string `json:"config_rev"`
	UsersRev  string `json:"users_rev"`
}

type SyncResponse struct {
	Machine string `json:"machine"`
	// Nodes 只包含有变化的节点；没有变化的节点不出现。
	Nodes []NodeSync `json:"nodes"`
	// Removed 是节点本地有、但已不属于这台机器的节点，应当停掉。
	Removed []int64 `json:"removed"`
	// Remote 是限制了设备数的用户在其他机器上的在线设备数，用于全局设备数限制。
	Remote   map[int64]int `json:"remote_devices,omitempty"`
	Settings Settings      `json:"settings"`
	// Forwards 是这台机器应有的完整转发列表（中转线路），见下文「中转转发」。
	Forwards *ForwardSync `json:"forwards,omitempty"`
}

// Settings 是面板下发的运行参数。
type Settings struct {
	ReportInterval int `json:"report_interval"` // 秒
	SyncInterval   int `json:"sync_interval"`   // 秒，WebSocket 断开时的轮询间隔
	DeviceTTL      int `json:"device_ttl"`      // 秒，多久没有连接的 IP 不再算作在线设备
	// ForwardMode 是这台机器的转发模式：auto（能用 nftables 就用）、nft、userspace。
	ForwardMode string `json:"forward_mode,omitempty"`
	// ProbeInterval 是对每条转发的下一跳做 TCP 探测的间隔（秒），0 为不探测。
	ProbeInterval int `json:"probe_interval,omitempty"`

	// ---- 落地访问统计 ----

	// Destinations 为 true 时节点嗅探目标域名，按「用户 × 目标」聚合流量，每 DestinationInterval 上报一次。
	Destinations bool `json:"destinations,omitempty"`
}

// NodeSync 是一个节点的变化。Config 为 nil 表示配置没变；Users 与 Delta 至多有一个，都为 nil 表示用户没变。
type NodeSync struct {
	ID        int64      `json:"id"`
	ConfigRev string     `json:"config_rev"`
	UsersRev  string     `json:"users_rev"`
	Config    *Node      `json:"config,omitempty"`
	Users     []User     `json:"users,omitempty"`
	Delta     *UserDelta `json:"delta,omitempty"`
	// FullUsers 为 true 时 Users 是全量列表（可能为空列表）。
	FullUsers bool `json:"full_users,omitempty"`
}

// UserDelta 是相对节点上报的 users_rev 的增量。
type UserDelta struct {
	Upsert []User  `json:"upsert,omitempty"`
	Remove []int64 `json:"remove,omitempty"`
}

// User 是一个可以连接节点的用户。Password 是该用户在这个节点上的凭证：
// 大多数协议就是 UUID，Shadowsocks 2022 是用户密钥。
type User struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	Password    string `json:"password"`
	SpeedLimit  int    `json:"speed_limit,omitempty"`  // Mbps，0 为不限
	DeviceLimit int    `json:"device_limit,omitempty"` // 0 为不限
}

// Node 是一个节点的完整配置，与内核无关。
type Node struct {
	ID       int64  `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"` // vless vmess trojan shadowsocks hysteria2 tuic anytls
	Listen   string `json:"listen"`
	Port     int    `json:"port"`

	TLS       *TLS       `json:"tls,omitempty"`
	Transport *Transport `json:"transport,omitempty"`
	Multiplex bool       `json:"multiplex,omitempty"`

	Flow       string   `json:"flow,omitempty"`          // vless
	Cipher     string   `json:"cipher,omitempty"`        // shadowsocks
	ServerKey  string   `json:"server_key,omitempty"`    // shadowsocks 2022
	UpMbps     int      `json:"up_mbps,omitempty"`       // hysteria2
	DownMbps   int      `json:"down_mbps,omitempty"`     // hysteria2
	Obfs       string   `json:"obfs,omitempty"`          // hysteria2：salamander
	ObfsPass   string   `json:"obfs_password,omitempty"` // hysteria2
	Congestion string   `json:"congestion,omitempty"`    // tuic
	Padding    []string `json:"padding,omitempty"`       // anytls
}

// TLS 的 Mode 为 tls 或 reality。
type TLS struct {
	Mode       string   `json:"mode"`
	ServerName string   `json:"server_name,omitempty"`
	ALPN       []string `json:"alpn,omitempty"`
	Cert       *Cert    `json:"cert,omitempty"`

	// Reality
	PrivateKey      string   `json:"private_key,omitempty"`
	ShortIDs        []string `json:"short_ids,omitempty"`
	HandshakeServer string   `json:"handshake_server,omitempty"`
	HandshakePort   int      `json:"handshake_port,omitempty"`
}

// Cert 说明证书从哪里来。Mode：
//
//	pem   面板集中申请后随配置下发
//	file  节点机器上已有的证书文件
//	acme  节点用 ACME 自行申请：http-01 / tls-alpn-01 不需要任何密钥；dns-01 只下发权限受限的子 token
//	self  节点生成自签名证书（客户端需允许不安全连接）
type Cert struct {
	Mode     string `json:"mode"`
	CertPEM  string `json:"cert_pem,omitempty"`
	KeyPEM   string `json:"key_pem,omitempty"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Email    string `json:"email,omitempty"`
	// DNSProvider 为 cloudflare 或 alidns；DNSCredentials 是该服务商的受限凭证。
	DNSProvider    string            `json:"dns_provider,omitempty"`
	DNSCredentials map[string]string `json:"dns_credentials,omitempty"`
}

// Transport 的 Type 为 ws、grpc、httpupgrade、http（h2）。
type Transport struct {
	Type        string            `json:"type"`
	Path        string            `json:"path,omitempty"`
	Host        []string          `json:"host,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	ServiceName string            `json:"service_name,omitempty"`
}

// ReportRequest 是一批上报。同一个 Spool 内 Seq 严格递增；面板按「机器 + Spool + Seq」去重，
// 收到重复的批次只回 ack 不重复入账。节点本地状态丢失后会换一个新的 Spool 重新从 1 计数。
type ReportRequest struct {
	Spool string       `json:"spool"`
	Seq   int64        `json:"seq"`
	At    time.Time    `json:"at"`
	Nodes []NodeReport `json:"nodes"`
	// Forwards 是转发的流量差值、应用结果和探测结果；Relay 是转发层的整体状态。
	Forwards []ForwardReport `json:"forwards,omitempty"`
	Relay    *RelayStatus    `json:"relay,omitempty"`
	// Listening 是本机正在监听的 TCP / UDP 端口，面板分配转发端口时避开。nil 表示本批没有采集。
	Listening []int `json:"listening,omitempty"`
}

type NodeReport struct {
	ID int64 `json:"id"`
	// Traffic 是用户 ID 到 [上行, 下行] 字节数。
	Traffic map[int64][2]int64 `json:"traffic,omitempty"`
	// Alive 是用户 ID 到当前在线的 IP。
	Alive  map[int64][]string `json:"alive,omitempty"`
	Status *NodeStatus        `json:"status,omitempty"`

	// ---- 落地访问统计（Settings.Destinations 开启时，每 DestinationInterval 出现一次）----

	Destinations []DestinationStat `json:"destinations,omitempty"`
	// DestinationsMerged 是这个周期内因超出 MaxDestinations 被合并进「其他」的条目数。
	DestinationsMerged int `json:"destinations_merged,omitempty"`
}

// DestinationInterval 是落地访问统计的上报周期；条目上限 MaxDestinations 也按这个周期计算。
const DestinationInterval = 5 * time.Minute

// MaxDestinations 是每个节点每个上报周期最多保留的「用户 × 目标」条目，超出的合并为「其他」。
const MaxDestinations = 5000

// 访问目标的类型：域名（可注册域名，例如 google.com）、IP，以及超出上限后合并的「其他」。
const (
	DestinationDomain = "domain"
	DestinationIP     = "ip"
	DestinationOther  = "other"
	// DestinationOtherName 是合并条目的目标名称。
	DestinationOtherName = "其他"
)

// DestinationStat 是一个上报周期内某个用户访问某个目标的聚合，不含连接级明细。
type DestinationStat struct {
	User  int64  `json:"user"`
	Kind  string `json:"kind"` // domain / ip / other
	Dest  string `json:"dest"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Conns int64  `json:"conns"`
	// Merged 只出现在「其他」条目上：这个用户被合并进来的目标数。
	Merged int64 `json:"merged,omitempty"`
}

// NodeStatus 是节点在内核里的运行状态。
type NodeStatus struct {
	Running       bool       `json:"running"`
	Error         string     `json:"error,omitempty"`
	Listening     *bool      `json:"listening,omitempty"` // 只对 TCP 协议有意义
	CertExpiresAt *time.Time `json:"cert_expires_at,omitempty"`
	Users         int        `json:"users"`
	Connections   int        `json:"connections"`
	AppliedAt     time.Time  `json:"applied_at"`
}

type ReportResponse struct {
	Ack       int64 `json:"ack"`
	Duplicate bool  `json:"duplicate,omitempty"`
}

// ---- 中转转发 --------------------------------------------------------------
//
// 中转机器上的 patato-node 按面板下发的完整列表维护转发：nft 模式独占 table inet patato，
// 每次原子替换；userspace 模式用内置 sing-box 的 direct 入站（标签 r<转发 ID>）。

// ForwardSync 是本机应有的完整转发列表。Rev 不变时列表不变，节点可以跳过应用。
type ForwardSync struct {
	Rev   string    `json:"rev"`
	Items []Forward `json:"items"`
}

// Forward 是一条转发：本机 ListenPort（TCP 与 UDP）转到 TargetIP:TargetPort。TargetIP 由面板解析好，节点不做 DNS。
type Forward struct {
	ID         int64  `json:"id"`
	ListenPort int    `json:"listen_port"`
	TargetIP   string `json:"target_ip"`
	TargetPort int    `json:"target_port"`
	// Mode 为 nft 或 userspace；auto 机器上 nft 不可用时节点自动退回 userspace，并在 ForwardReport.Mode 里上报实际模式。
	Mode string `json:"mode"`
}

// ForwardReport 是一条转发在一个上报周期里的情况。Up / Down 是字节差值（客户端到下一跳为 Up），
// 计数器读数变小（重启、表重建）视为归零，从新读数开始累计。
type ForwardReport struct {
	ID      int64     `json:"id"`
	Up      int64     `json:"up,omitempty"`
	Down    int64     `json:"down,omitempty"`
	Applied bool      `json:"applied"`
	Mode    string    `json:"mode,omitempty"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at,omitempty"`
	// Probe 是本周期内对下一跳的 TCP 探测结果，没有探测时为 nil。
	Probe *ProbeResult `json:"probe,omitempty"`
}

// ProbeResult 汇总一个上报周期内的 TCP 探测：成功、失败次数，成功时的平均耗时，以及截至本周期末的连续失败次数。
type ProbeResult struct {
	OK          int       `json:"ok"`
	Fail        int       `json:"fail"`
	AvgMS       int       `json:"avg_ms,omitempty"`
	Consecutive int       `json:"consecutive_failures"`
	Last        bool      `json:"last_ok"`
	At          time.Time `json:"at"`
}

// RelayStatus 是转发层的整体状态：实际模式和前置条件问题（例如 ip_forward 没开）。
type RelayStatus struct {
	Mode  string `json:"mode"`
	Error string `json:"error,omitempty"`
}

// Push 是 WebSocket 上的消息。
type Push struct {
	Type string `json:"type"` // changed / ping
}

// Error 是协议错误的报文，与管理接口的错误格式一致。
type Error struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Hint      string `json:"hint,omitempty"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

// Rev 是内容的版本号：内容不变，版本号不变。
func Rev(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// UsersRev 是用户列表的版本号，与顺序无关。节点应用增量后用它自校验，不一致就要全量。
func UsersRev(users []User) string {
	sorted := slices.Clone(users)
	slices.SortFunc(sorted, func(a, b User) int { return cmp.Compare(a.ID, b.ID) })
	return Rev(sorted)
}
