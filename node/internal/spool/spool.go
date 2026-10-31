// Package spool 是 patato-node 的本地上报队列：每批上报先落盘再发送，面板确认后才删除，
// 节点重启或面板暂时不可用都不会丢流量。
//
// 队列有一个随机 ID，序号在队列内严格递增；面板按「机器 + 队列 ID + 序号」去重，所以重发是安全的。
// 本地状态丢失（例如重装）时会生成新的队列 ID，从 1 重新计数。
package spool

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/momaek/patato/nodeapi"
)

// MaxPending 是最多保留的未确认批次，按每分钟一批约一周。超出时丢弃最旧的批次并记录错误。
const MaxPending = 10000

type Spool struct {
	dir  string
	log  *slog.Logger
	mu   sync.Mutex
	meta meta
}

type meta struct {
	ID   string `json:"id"`
	Next int64  `json:"next"`
}

func Open(dir string, log *slog.Logger) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Spool{dir: dir, log: log}
	b, err := os.ReadFile(s.metaPath())
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.meta); err != nil || s.meta.ID == "" || s.meta.Next < 1 {
			return nil, fmt.Errorf("spool metadata %s is corrupt; move it away to start a new spool", s.metaPath())
		}
	case os.IsNotExist(err):
		id := make([]byte, 8)
		_, _ = rand.Read(id)
		s.meta = meta{ID: hex.EncodeToString(id), Next: 1}
		if err := s.saveMeta(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

func (s *Spool) ID() string { return s.meta.ID }

func (s *Spool) metaPath() string { return filepath.Join(s.dir, "spool.json") }

func (s *Spool) saveMeta() error {
	b, _ := json.Marshal(s.meta)
	return writeAtomic(s.metaPath(), b)
}

func (s *Spool) file(seq int64) string { return filepath.Join(s.dir, fmt.Sprintf("%012d.json", seq)) }

// Append 给一批上报分配序号并落盘。
func (s *Spool) Append(r nodeapi.ReportRequest) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.Spool, r.Seq = s.meta.ID, s.meta.Next
	s.meta.Next++
	// 先推进序号再写批次：中途崩溃最多留下一个空号，不会重复使用序号
	if err := s.saveMeta(); err != nil {
		return 0, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	if err := writeAtomic(s.file(r.Seq), b); err != nil {
		return 0, err
	}
	seqs, err := s.seqs()
	if err == nil && len(seqs) > MaxPending {
		for _, old := range seqs[:len(seqs)-MaxPending] {
			s.log.Error("report spool full, dropping oldest batch", "seq", old)
			_ = os.Remove(s.file(old))
		}
	}
	return r.Seq, nil
}

// Pending 按序号返回所有未确认的批次。
func (s *Spool) Pending() ([]nodeapi.ReportRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seqs, err := s.seqs()
	if err != nil {
		return nil, err
	}
	out := make([]nodeapi.ReportRequest, 0, len(seqs))
	for _, seq := range seqs {
		b, err := os.ReadFile(s.file(seq))
		if err != nil {
			return nil, err
		}
		var r nodeapi.ReportRequest
		if err := json.Unmarshal(b, &r); err != nil {
			s.log.Error("drop corrupt report batch", "seq", seq, "err", err)
			_ = os.Remove(s.file(seq))
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Ack 删除序号不大于 seq 的批次。
func (s *Spool) Ack(seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seqs, err := s.seqs()
	if err != nil {
		return err
	}
	for _, n := range seqs {
		if n <= seq {
			if err := os.Remove(s.file(n)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (s *Spool) seqs() ([]int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || name == "spool" {
			continue
		}
		if n, err := strconv.ParseInt(name, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
