package api

// 落地访问统计：节点视图（按用户求和）与用户视图，只读。数据由 patato-node 上报，见 server/internal/native/destinations.go。

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

type destinationView struct {
	Kind   string `json:"kind"`
	Dest   string `json:"dest"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
	Bytes  int64  `json:"bytes"`
	Conns  int64  `json:"conns"`
	Merged int64  `json:"merged,omitempty"`
	Users  int64  `json:"users,omitempty"`
	Nodes  int64  `json:"nodes,omitempty"`
}

// destinationsSince 解析 since（24h、7d、90m，默认 24h），不超过保留天数。返回整点起点与规范化的写法。
func (s *Server) destinationsSince(c *gin.Context) (time.Time, string, bool) {
	raw := strings.TrimSpace(c.DefaultQuery("since", "24h"))
	var d time.Duration
	var err error
	if n, ok := strings.CutSuffix(raw, "d"); ok {
		var days int
		days, err = strconv.Atoi(n)
		d = time.Duration(days) * 24 * time.Hour
	} else {
		d, err = time.ParseDuration(raw)
	}
	if err != nil || d <= 0 {
		fail(c, apierr.BadRequest("invalid_since", "since 写成 24h、7d 这样的时长").WithHint("例如 ?since=24h"))
		return time.Time{}, "", false
	}
	max := time.Duration(s.d.Cfg.Monitoring.Destinations.RetentionDays) * 24 * time.Hour
	if d > max {
		d = max
	}
	return time.Now().Add(-d).Truncate(time.Hour), raw, true
}

func (s *Server) findUserRef(c *gin.Context, key string) (db.User, bool) {
	var u db.User
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		u, err = s.d.Store.GetUser(c, id)
	} else {
		u, err = s.d.Store.GetUserByEmail(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("user_not_found", "用户 %s 不存在", key).WithHint("用 GET /api/v1/users?q=<邮箱> 搜索"))
		return u, false
	}
	if err != nil {
		fail(c, err)
		return u, false
	}
	return u, true
}

func (s *Server) findNodeRef(c *gin.Context, key string) (db.Node, bool) {
	var n db.Node
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		n, err = s.d.Store.GetNode(c, id)
	} else {
		n, err = s.d.Store.GetNodeByCode(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("node_not_found", "节点 %s 不存在", key).WithHint("用 GET /api/v1/nodes 查看全部节点"))
		return n, false
	}
	if err != nil {
		fail(c, err)
		return n, false
	}
	return n, true
}

func (s *Server) destinationsMeta(since time.Time, sinceRaw string, totals db.DestinationTotalsRow) gin.H {
	return gin.H{
		"enabled":        s.d.Cfg.Monitoring.Destinations.Enabled,
		"retention_days": s.d.Cfg.Monitoring.Destinations.RetentionDays,
		"since":          since,
		"period":         sinceRaw,
		"total":          gin.H{"up": totals.Up, "down": totals.Down, "bytes": totals.Up + totals.Down, "conns": totals.Conns, "destinations": totals.Destinations},
	}
}

// destinationStatsNotice 是用户侧「我的订阅」页的告知；未开启时为 nil，页面不显示。
func (s *Server) destinationStatsNotice() gin.H {
	d := s.d.Cfg.Monitoring.Destinations
	if !d.Enabled {
		return nil
	}
	return gin.H{"retention_days": d.RetentionDays}
}

// nodeDestinations 是节点上访问最多的目标，按用户求和；可用 user 只看一个用户。
func (s *Server) nodeDestinations(c *gin.Context) {
	n, found := s.findNode(c)
	if !found {
		return
	}
	since, raw, valid := s.destinationsSince(c)
	if !valid {
		return
	}
	var userID *int64
	if key := strings.TrimSpace(c.Query("user")); key != "" {
		u, found := s.findUserRef(c, key)
		if !found {
			return
		}
		userID = &u.ID
	}
	limit := queryInt(c, "limit", 50, 1, 200)
	rows, err := s.d.Store.TopNodeDestinations(c, db.TopNodeDestinationsParams{NodeID: n.ID, Since: since, UserID: userID, RowLimit: int32(limit)})
	if err != nil {
		fail(c, err)
		return
	}
	totals, err := s.d.Store.DestinationTotals(c, db.DestinationTotalsParams{Since: since, NodeID: &n.ID, UserID: userID})
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]destinationView, 0, len(rows))
	for _, r := range rows {
		items = append(items, destinationView{Kind: r.Kind, Dest: r.Dest, Up: r.Up, Down: r.Down, Bytes: r.Up + r.Down,
			Conns: r.Conns, Merged: r.Merged, Users: r.Users})
	}
	out := s.destinationsMeta(since, raw, totals)
	out["node"] = ref{ID: n.ID, Code: n.Code, Name: n.Name}
	out["user_id"] = userID
	out["items"] = items
	ok(c, out)
}

// userDestinations 是用户访问最多的目标；可用 node 只看一个节点。
func (s *Server) userDestinations(c *gin.Context) {
	u, found := s.findUserRef(c, c.Param("id"))
	if !found {
		return
	}
	since, raw, valid := s.destinationsSince(c)
	if !valid {
		return
	}
	var nodeID *int64
	if key := strings.TrimSpace(c.Query("node")); key != "" {
		n, found := s.findNodeRef(c, key)
		if !found {
			return
		}
		nodeID = &n.ID
	}
	limit := queryInt(c, "limit", 50, 1, 200)
	rows, err := s.d.Store.TopUserDestinations(c, db.TopUserDestinationsParams{UserID: u.ID, Since: since, NodeID: nodeID, RowLimit: int32(limit)})
	if err != nil {
		fail(c, err)
		return
	}
	totals, err := s.d.Store.DestinationTotals(c, db.DestinationTotalsParams{Since: since, NodeID: nodeID, UserID: &u.ID})
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]destinationView, 0, len(rows))
	for _, r := range rows {
		items = append(items, destinationView{Kind: r.Kind, Dest: r.Dest, Up: r.Up, Down: r.Down, Bytes: r.Up + r.Down,
			Conns: r.Conns, Merged: r.Merged, Nodes: r.Nodes})
	}
	out := s.destinationsMeta(since, raw, totals)
	out["user"] = gin.H{"id": u.ID, "email": u.Email}
	out["node_id"] = nodeID
	out["items"] = items
	ok(c, out)
}
