package rules

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

const (
	// maxSize 是单个文件的大小上限；ACL4SSR 最大的 BanEasyList 约 1.3 MB。
	maxSize = 16 << 20
	// staleAfter 之后重新拉取模板和规则文件。
	staleAfter = 6 * time.Hour
)

// ErrNotFound 表示请求的规则文件不在任何模板里。
var ErrNotFound = errors.New("rule list not found")

// Service 拉取并缓存规则模板与规则文件。地址都来自管理员配置的模板，不接受用户提供的地址。
type Service struct {
	st     *store.Store
	client *http.Client
	log    *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewService(st *store.Store, log *slog.Logger) *Service {
	return &Service{st: st, client: &http.Client{Timeout: 30 * time.Second}, log: log, locks: map[string]*sync.Mutex{}}
}

// Fetch 下载一个文件。
func (s *Service) Fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "patato-rules")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxSize {
		return "", fmt.Errorf("文件超过 %d MB", maxSize>>20)
	}
	return string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))), nil
}

// FetchProfile 下载并解析 ini 模板。
func (s *Service) FetchProfile(ctx context.Context, url string) (string, *Profile, error) {
	content, err := s.Fetch(ctx, url)
	if err != nil {
		return "", nil, fmt.Errorf("下载 %s 失败：%w", url, err)
	}
	p, err := Parse(content)
	if err != nil {
		return "", nil, fmt.Errorf("%s 不是可用的规则模板：%w", url, err)
	}
	return content, p, nil
}

// EnsureLists 登记模板引用的规则文件，客户端第一次请求时再下载。
func EnsureLists(ctx context.Context, q *db.Queries, p *Profile) error {
	for _, l := range p.Lists() {
		if err := q.EnsureRuleList(ctx, db.EnsureRuleListParams{Key: l.Key, Url: l.URL}); err != nil {
			return err
		}
	}
	return nil
}

// ProfileFor 返回用户实际使用的规则：自选的（且仍允许自选）优先，其次是套餐默认；都没有时返回 nil，用内置规则。
func ProfileFor(ctx context.Context, q *db.Queries, u db.User) (*db.RuleProfile, error) {
	if u.RuleProfile != nil {
		rp, err := q.GetRuleProfileByCode(ctx, *u.RuleProfile)
		if err == nil && rp.UserSelectable {
			return &rp, nil
		}
		if err != nil && !store.IsNotFound(err) {
			return nil, err
		}
	}
	if u.PlanID == nil {
		return nil, nil
	}
	pl, err := q.GetPlan(ctx, *u.PlanID)
	if err != nil || pl.RuleProfile == nil {
		return nil, err
	}
	rp, err := q.GetRuleProfileByCode(ctx, *pl.RuleProfile)
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rp, nil
}

// List 返回规则文件的内容。还没下载过时现在下载；下载失败但有旧内容时返回旧内容。
func (s *Service) List(ctx context.Context, key string) (db.RuleList, error) {
	l, err := s.st.GetRuleList(ctx, key)
	if store.IsNotFound(err) {
		return l, ErrNotFound
	}
	if err != nil || l.FetchedAt != nil {
		return l, err
	}
	lock := s.lock(key)
	lock.Lock()
	defer lock.Unlock()
	if l, err = s.st.GetRuleList(ctx, key); err != nil || l.FetchedAt != nil {
		return l, err
	}
	if err := s.refreshList(ctx, l.Key, l.Url); err != nil {
		return l, err
	}
	return s.st.GetRuleList(ctx, key)
}

func (s *Service) lock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	return m
}

func (s *Service) refreshList(ctx context.Context, key, url string) error {
	content, err := s.Fetch(ctx, url)
	if err != nil {
		s.log.Warn("fetch rule list", "url", url, "err", err)
		_ = s.st.SetRuleListError(ctx, db.SetRuleListErrorParams{Key: key, FetchError: err.Error()})
		return err
	}
	return s.st.SetRuleListContent(ctx, db.SetRuleListContentParams{Key: key, Content: content})
}

// Refresh 重新拉取过期的模板和规则文件，并清理不再被任何模板引用的规则文件。
// 拉取失败时保留上一次的内容，订阅照常可用。
func (s *Service) Refresh(ctx context.Context) error {
	return s.refresh(ctx, false)
}

// RefreshAll 和 Refresh 一样，但不管是否过期都重新拉取。
func (s *Service) RefreshAll(ctx context.Context) error {
	return s.refresh(ctx, true)
}

func (s *Service) refresh(ctx context.Context, force bool) error {
	now := time.Now()
	stale := func(t *time.Time) bool { return force || t == nil || now.Sub(*t) > staleAfter }
	profiles, err := s.st.ListRuleProfiles(ctx)
	if err != nil {
		return err
	}
	keys := []string{}
	for _, rp := range profiles {
		content := rp.Content
		if rp.SourceUrl != "" && stale(rp.FetchedAt) {
			fresh, _, err := s.FetchProfile(ctx, rp.SourceUrl)
			if err != nil {
				s.log.Warn("refresh rule profile", "code", rp.Code, "err", err)
				_ = s.st.SetRuleProfileFetchError(ctx, db.SetRuleProfileFetchErrorParams{ID: rp.ID, FetchError: err.Error()})
			} else {
				content = fresh
				if err := s.st.SetRuleProfileFetched(ctx, db.SetRuleProfileFetchedParams{ID: rp.ID, Content: fresh}); err != nil {
					return err
				}
			}
		}
		p, err := Parse(content)
		if err != nil {
			continue
		}
		if err := EnsureLists(ctx, s.st.Queries, p); err != nil {
			return err
		}
		for _, l := range p.Lists() {
			keys = append(keys, l.Key)
		}
	}
	if _, err := s.st.DeleteRuleListsExcept(ctx, keys); err != nil {
		return err
	}
	lists, err := s.st.ListRuleLists(ctx)
	if err != nil {
		return err
	}
	for _, l := range lists {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stale(l.FetchedAt) {
			_ = s.refreshList(ctx, l.Key, l.Url)
		}
	}
	return nil
}
