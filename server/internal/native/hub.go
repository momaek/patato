package native

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/store/db"
)

// Hub 维护 patato-node 的 WebSocket 连接，在机器的节点配置或用户列表变化时推送 changed。
//
// 变化的来源有两类：数据库触发器在相关表变化时发出 NOTIFY patato_nodes（每个实例都会收到）；
// 用户到期这类「时间到了就变」的情况没有写操作，由定时复查兜底。推送只是提示，节点收到后自己调用 sync。
type Hub struct {
	h        *Handler
	mu       sync.Mutex
	conns    map[int64]map[*wsConn]bool // 机器 ID → 连接
	last     map[int64]string           // 机器 ID → 最近一次推送时的状态指纹
	kick     chan struct{}
	Recheck  time.Duration
	upgrader websocket.Upgrader
}

type wsConn struct {
	machine db.Machine
	conn    *websocket.Conn
	send    chan nodeapi.Push
}

func newHub(h *Handler) *Hub {
	return &Hub{
		h: h, conns: map[int64]map[*wsConn]bool{}, last: map[int64]string{}, kick: make(chan struct{}, 1),
		Recheck: 30 * time.Second,
		// 节点不是浏览器，没有 Origin；鉴权靠机器 token
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
}

// Run 监听数据库通知并定时复查，直到 ctx 结束。
func (hub *Hub) Run(ctx context.Context) {
	go hub.listen(ctx)
	tick := time.NewTicker(hub.Recheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			hub.closeAll()
			return
		case <-hub.kick:
			// 合并短时间内的多次通知
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			hub.check(ctx)
		case <-tick.C:
			hub.check(ctx)
		}
	}
}

// Changed 让 Hub 尽快复查一次。
func (hub *Hub) Changed() {
	select {
	case hub.kick <- struct{}{}:
	default:
	}
}

func (hub *Hub) listen(ctx context.Context) {
	for ctx.Err() == nil {
		err := hub.listenOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		hub.h.log.Warn("listen patato_nodes", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (hub *Hub) listenOnce(ctx context.Context) error {
	conn, err := hub.h.st.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN patato_nodes"); err != nil {
		return err
	}
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		hub.Changed()
	}
}

func (hub *Hub) check(ctx context.Context) {
	hub.mu.Lock()
	machines := map[int64]db.Machine{}
	for id, set := range hub.conns {
		for c := range set {
			machines[id] = c.machine
			break
		}
	}
	hub.mu.Unlock()
	for id, m := range machines {
		states, err := hub.h.machineState(ctx, m.Host)
		if err != nil {
			hub.h.log.Warn("hub machine state", "machine", m.Host, "err", err)
			continue
		}
		fp := fingerprint(states)
		hub.mu.Lock()
		if hub.last[id] != fp {
			hub.last[id] = fp
			for c := range hub.conns[id] {
				c.push(nodeapi.Push{Type: "changed"})
			}
		}
		hub.mu.Unlock()
	}
}

func (c *wsConn) push(p nodeapi.Push) {
	select {
	case c.send <- p:
	default: // 已有未发出的提示，合并
	}
}

// WS 升级为 WebSocket。连上后先推送一次 changed，避免节点在 sync 与连上之间错过变化。
func (h *Handler) WS(c *gin.Context) {
	m := machineOf(c)
	conn, err := h.hub.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := &wsConn{machine: m, conn: conn, send: make(chan nodeapi.Push, 1)}
	hub := h.hub
	hub.mu.Lock()
	if hub.conns[m.ID] == nil {
		hub.conns[m.ID] = map[*wsConn]bool{}
	}
	hub.conns[m.ID][wc] = true
	hub.mu.Unlock()
	wc.push(nodeapi.Push{Type: "changed"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.SetReadLimit(4096)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ping := time.NewTicker(30 * time.Second)
	defer func() {
		ping.Stop()
		hub.mu.Lock()
		delete(hub.conns[m.ID], wc)
		if len(hub.conns[m.ID]) == 0 {
			delete(hub.conns, m.ID)
			delete(hub.last, m.ID)
		}
		hub.mu.Unlock()
		_ = conn.Close()
	}()
	for {
		select {
		case <-done:
			return
		case <-c.Request.Context().Done():
			return
		case p := <-wc.send:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(p); err != nil {
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		}
	}
}

func (hub *Hub) closeAll() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for _, set := range hub.conns {
		for c := range set {
			_ = c.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"), time.Now().Add(time.Second))
			_ = c.conn.Close()
		}
	}
}

// Connected 返回当前连着 WebSocket 的机器 ID，用于 Admin 展示。
func (hub *Hub) Connected() map[int64]bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	out := make(map[int64]bool, len(hub.conns))
	for id := range hub.conns {
		out[id] = true
	}
	return out
}
