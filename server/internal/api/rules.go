package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/rules"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

type ruleListView struct {
	Name       string     `json:"name"`
	URL        string     `json:"url"`
	FetchedAt  *time.Time `json:"fetched_at"`
	FetchError string     `json:"fetch_error,omitempty"`
}

type ruleProfileView struct {
	ID             int64          `json:"id"`
	Code           string         `json:"code"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	SourceURL      string         `json:"source_url"`
	UserSelectable bool           `json:"user_selectable"`
	FetchedAt      *time.Time     `json:"fetched_at"`
	FetchError     string         `json:"fetch_error,omitempty"`
	Groups         []string       `json:"groups"`
	RuleCount      int            `json:"rule_count"`
	ListCount      int            `json:"list_count"`
	Warnings       []string       `json:"warnings,omitempty"`
	ParseError     string         `json:"parse_error,omitempty"`
	PlanCount      *int64         `json:"plan_count,omitempty"`
	UserCount      *int64         `json:"user_count,omitempty"`
	Content        string         `json:"content,omitempty"`
	Lists          []ruleListView `json:"lists,omitempty"`
	Version        int64          `json:"version"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func ruleProfileViewOf(rp db.RuleProfile) (ruleProfileView, *rules.Profile) {
	v := ruleProfileView{ID: rp.ID, Code: rp.Code, Name: rp.Name, Description: rp.Description, SourceURL: rp.SourceUrl,
		UserSelectable: rp.UserSelectable, FetchedAt: rp.FetchedAt, FetchError: rp.FetchError, Groups: []string{},
		Version: rp.Version, UpdatedAt: rp.UpdatedAt}
	p, err := rules.Parse(rp.Content)
	if err != nil {
		v.ParseError = err.Error()
		return v, nil
	}
	for _, g := range p.Groups {
		v.Groups = append(v.Groups, g.Name)
	}
	v.RuleCount, v.ListCount, v.Warnings = len(p.Rulesets), len(p.Lists()), p.Warnings
	return v, p
}

func (s *Server) ruleProfileList(c *gin.Context) {
	rows, err := s.d.Store.ListRuleProfiles(c)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]ruleProfileView, 0, len(rows))
	for _, r := range rows {
		v, _ := ruleProfileViewOf(db.RuleProfile{ID: r.ID, Code: r.Code, Name: r.Name, Description: r.Description, SourceUrl: r.SourceUrl,
			Content: r.Content, UserSelectable: r.UserSelectable, FetchedAt: r.FetchedAt, FetchError: r.FetchError,
			Version: r.Version, UpdatedAt: r.UpdatedAt})
		plans, users := r.PlanCount, r.UserCount
		v.PlanCount, v.UserCount = &plans, &users
		items = append(items, v)
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) ruleProfilePresets(c *gin.Context) {
	ok(c, gin.H{"items": rules.Presets})
}

func (s *Server) ruleProfileGet(c *gin.Context) {
	key := c.Param("id")
	var rp db.RuleProfile
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		rp, err = s.d.Store.GetRuleProfile(c, id)
	} else {
		rp, err = s.d.Store.GetRuleProfileByCode(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("rule_profile_not_found", "分流规则 %s 不存在", key).WithHint("用 GET /api/v1/rule-profiles 查看全部"))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	v, p := ruleProfileViewOf(rp)
	v.Content = rp.Content
	users, err := s.d.Store.CountRuleProfileUsers(c, rp.Code)
	if err != nil {
		fail(c, err)
		return
	}
	v.UserCount = &users
	if p != nil {
		cached := map[string]db.ListRuleListsRow{}
		if rows, err := s.d.Store.ListRuleLists(c); err == nil {
			for _, r := range rows {
				cached[r.Key] = r
			}
		}
		v.Lists = []ruleListView{}
		for _, l := range p.Lists() {
			lv := ruleListView{Name: l.Name, URL: l.URL}
			if r, ok := cached[l.Key]; ok {
				lv.FetchedAt, lv.FetchError = r.FetchedAt, r.FetchError
			}
			v.Lists = append(v.Lists, lv)
		}
	}
	ok(c, v)
}

// ---- 用户侧 ---------------------------------------------------------------

type meRuleProfile struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Groups      []string `json:"groups"`
}

func meRuleProfileOf(rp db.RuleProfile) meRuleProfile {
	v, _ := ruleProfileViewOf(rp)
	return meRuleProfile{Code: rp.Code, Name: rp.Name, Description: rp.Description, Groups: v.Groups}
}

// meRuleProfiles 返回可以自选的分流规则、套餐默认规则和当前生效的规则。
func (s *Server) meRuleProfiles(c *gin.Context) {
	u := meUser(c)
	rows, err := s.d.Store.ListSelectableRuleProfiles(c)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]meRuleProfile, 0, len(rows))
	for _, rp := range rows {
		if rp.Content != "" {
			items = append(items, meRuleProfileOf(rp))
		}
	}
	var planDefault, effective *meRuleProfile
	if u.PlanID != nil {
		if pl, err := s.d.Store.GetPlan(c, *u.PlanID); err == nil && pl.RuleProfile != nil {
			if rp, err := s.d.Store.GetRuleProfileByCode(c, *pl.RuleProfile); err == nil {
				v := meRuleProfileOf(rp)
				planDefault = &v
			}
		}
	}
	if rp, err := rules.ProfileFor(c, s.d.Store.Queries, u); err == nil && rp != nil {
		v := meRuleProfileOf(*rp)
		effective = &v
	}
	ok(c, gin.H{"selected": u.RuleProfile, "plan_default": planDefault, "effective": effective, "items": items})
}

// meSetRuleProfile 由用户本人选择分流规则；空字符串表示跟随套餐默认。只能选管理员允许自选的规则。
func (s *Server) meSetRuleProfile(c *gin.Context) {
	u := meUser(c)
	var req struct {
		RuleProfile string `json:"rule_profile"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	var code *string
	label := "跟随套餐默认"
	if req.RuleProfile != "" {
		rp, err := s.d.Store.GetRuleProfileByCode(c, req.RuleProfile)
		if store.IsNotFound(err) || (err == nil && !rp.UserSelectable) {
			fail(c, apierr.BadRequest("rule_profile_unavailable", "分流规则 %s 不可选", req.RuleProfile).
				WithHint("用 GET /api/v1/me/rule-profiles 查看可以选择的规则"))
			return
		}
		if err != nil {
			fail(c, err)
			return
		}
		code, label = &rp.Code, rp.Name
	}
	err := s.d.Store.Tx(c, func(q *db.Queries, _ pgx.Tx) error {
		if err := q.SetUserRuleProfile(c, db.SetUserRuleProfileParams{ID: u.ID, RuleProfile: code}); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"summary": fmt.Sprintf("用户本人把分流规则改为%s", label), "rule_profile": code})
		return q.InsertAudit(c, db.InsertAuditParams{
			SubjectKind: "user", SubjectID: u.ID, SubjectLabel: u.Email, Entry: "portal", Action: "user.set_rule_profile",
			Target: fmt.Sprintf("user:%d", u.ID), Risk: "normal", Result: "success", Detail: detail,
		})
	})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"rule_profile": code})
}
