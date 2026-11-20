package main

// 落地访问统计：patato node destinations <节点> [--user <用户>] [--since 24h]

import (
	"fmt"
	"strconv"

	"github.com/momaek/patato/cli/internal/client"
)

const epNodeDestinations = "/api/v1/nodes/{id}/destinations"

// 放在这里而不是 endpoints.go 的列表里，契约测试同样会检查。
func init() { usedEndpoints = append(usedEndpoints, "GET "+epNodeDestinations) }

type destinationRanking struct {
	Enabled       bool   `json:"enabled"`
	RetentionDays int    `json:"retention_days"`
	Period        string `json:"period"`
	Node          ref    `json:"node"`
	Total         struct {
		Bytes        int64 `json:"bytes"`
		Conns        int64 `json:"conns"`
		Destinations int64 `json:"destinations"`
	} `json:"total"`
	Items []struct {
		Kind   string `json:"kind"`
		Dest   string `json:"dest"`
		Up     int64  `json:"up"`
		Down   int64  `json:"down"`
		Bytes  int64  `json:"bytes"`
		Conns  int64  `json:"conns"`
		Merged int64  `json:"merged"`
		Users  int64  `json:"users"`
	} `json:"items"`
}

var destKindNames = map[string]string{"domain": "域名", "ip": "IP", "other": "合并"}

func (a *app) nodeDestinations(args []string) error {
	c := newCommand("node destinations", "patato node destinations <节点> [--user <用户>] [--since 24h] [--limit 50]\n\n"+
		"节点上访问最多的域名和 IP（按用户求和；域名记主域名，不含路径）。需要服务端开启 monitoring.destinations。")
	user := c.fs.String("user", "", "只看这个用户（ID 或邮箱）")
	since := c.fs.String("since", "24h", "时间段，例如 24h、7d；不超过保留天数")
	limit := c.fs.Int("limit", 50, "最多显示多少条（≤200）")
	if err := c.parse(args); err != nil {
		return err
	}
	node, err := c.arg(0, "节点")
	if err != nil {
		return err
	}
	if err := c.noExtra(1); err != nil {
		return err
	}
	var r destinationRanking
	q := query("user", *user, "since", *since, "limit", strconv.Itoa(*limit))
	if printed, err := a.get(client.Path(epNodeDestinations, node), q, &r); printed || err != nil {
		return err
	}
	if !r.Enabled {
		a.printf("提示：服务端未开启落地访问统计（monitoring.destinations.enabled），不会有新数据。\n\n")
	}
	if len(r.Items) == 0 {
		a.printf("%s 最近 %s 没有访问记录\n", r.Node.Code, r.Period)
		return nil
	}
	a.printf("%s 最近 %s：%s，%d 个连接，%d 个目标（保留 %d 天）\n\n", r.Node.Code, r.Period, bytesText(r.Total.Bytes),
		r.Total.Conns, r.Total.Destinations, r.RetentionDays)
	rows := [][]string{}
	for i, d := range r.Items {
		share := "—"
		if r.Total.Bytes > 0 {
			share = fmt.Sprintf("%.1f%%", float64(d.Bytes)*100/float64(r.Total.Bytes))
		}
		dest := d.Dest
		if d.Merged > 0 {
			dest = fmt.Sprintf("%s（%d 个目标）", d.Dest, d.Merged)
		}
		rows = append(rows, []string{strconv.Itoa(i + 1), dest, label(destKindNames, d.Kind), bytesText(d.Bytes), share,
			bytesText(d.Up), bytesText(d.Down), strconv.FormatInt(d.Conns, 10), strconv.FormatInt(d.Users, 10)})
	}
	table(a.out, []string{"#", "目标", "类型", "流量", "占比", "上行", "下行", "连接", "用户"}, rows, 0, 3, 4, 5, 6, 7, 8)
	return nil
}
