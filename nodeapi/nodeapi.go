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

// EnrollRequest 凭注册码换取机器 token。注册码由 patato machine install-cmd 生成，一小时内有效，只能用一次。
type EnrollRequest struct {
	Code     string `json:"code"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
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
}

// Settings 是面板下发的运行参数。
type Settings struct {
	ReportInterval int `json:"report_interval"` // 秒
	SyncInterval   int `json:"sync_interval"`   // 秒，WebSocket 断开时的轮询间隔
	DeviceTTL      int `json:"device_ttl"`      // 秒，多久没有连接的 IP 不再算作在线设备
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
}

type NodeReport struct {
	ID int64 `json:"id"`
	// Traffic 是用户 ID 到 [上行, 下行] 字节数。
	Traffic map[int64][2]int64 `json:"traffic,omitempty"`
	// Alive 是用户 ID 到当前在线的 IP。
	Alive  map[int64][]string `json:"alive,omitempty"`
	Status *NodeStatus        `json:"status,omitempty"`
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
