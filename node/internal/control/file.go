package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/momaek/patato/nodeapi"
)

// LocalState 是独立运行时的本地文件格式：节点配置和用户直接写在文件里。
type LocalState struct {
	Machine  string           `json:"machine"`
	Nodes    []LocalNode      `json:"nodes"`
	Settings nodeapi.Settings `json:"settings"`
}

type LocalNode struct {
	Config nodeapi.Node   `json:"config"`
	Users  []nodeapi.User `json:"users"`
}

// File 从本地文件读取配置，上报追加写到另一个文件（每行一个 JSON），便于测试和排障。
// 改动文件后几秒内自动生效。
type File struct {
	path    string
	reports string
	mu      sync.Mutex
}

func NewFile(path, reports string) *File { return &File{path: path, reports: reports} }

func (f *File) load() (*LocalState, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	var st LocalState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", f.path, err)
	}
	return &st, nil
}

func (f *File) Sync(_ context.Context, req nodeapi.SyncRequest) (*nodeapi.SyncResponse, error) {
	st, err := f.load()
	if err != nil {
		return nil, err
	}
	resp := &nodeapi.SyncResponse{Machine: firstNonEmpty(st.Machine, "local"), Nodes: []nodeapi.NodeSync{}, Removed: []int64{}, Settings: st.Settings}
	seen := map[int64]bool{}
	for _, n := range st.Nodes {
		seen[n.Config.ID] = true
		users := n.Users
		if users == nil {
			users = []nodeapi.User{}
		}
		for i := range users {
			if users[i].Password == "" {
				users[i].Password = users[i].UUID
			}
		}
		ns := nodeapi.NodeSync{ID: n.Config.ID, ConfigRev: nodeapi.Rev(n.Config), UsersRev: nodeapi.UsersRev(users)}
		known := req.Nodes[n.Config.ID]
		changed := false
		if known.ConfigRev != ns.ConfigRev {
			cfg := n.Config
			ns.Config, changed = &cfg, true
		}
		if known.UsersRev != ns.UsersRev {
			ns.Users, ns.FullUsers, changed = users, true, true
		}
		if changed {
			resp.Nodes = append(resp.Nodes, ns)
		}
	}
	for id := range req.Nodes {
		if !seen[id] {
			resp.Removed = append(resp.Removed, id)
		}
	}
	return resp, nil
}

func (f *File) Report(_ context.Context, req nodeapi.ReportRequest) (*nodeapi.ReportResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reports != "" {
		b, _ := json.Marshal(req)
		fh, err := os.OpenFile(f.reports, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		defer fh.Close()
		if _, err := fh.Write(append(b, '\n')); err != nil {
			return nil, err
		}
	}
	return &nodeapi.ReportResponse{Ack: req.Seq}, nil
}

// Watch 每两秒看一次文件有没有改过。
func (f *File) Watch(ctx context.Context, changed func()) {
	var last time.Time
	if fi, err := os.Stat(f.path); err == nil {
		last = fi.ModTime()
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if fi, err := os.Stat(f.path); err == nil && !fi.ModTime().Equal(last) {
				last = fi.ModTime()
				changed()
			}
		}
	}
}
