package api

import (
	"bytes"
	_ "embed"
	"net/http"
	"text/template"

	"github.com/gin-gonic/gin"

	patatoapi "github.com/momaek/patato/api"
)

//go:embed skill.md
var skillTemplate string

var skillTpl = template.Must(template.New("skill").Parse(skillTemplate))

type tool struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Risk    string `json:"risk"`
	Access  string `json:"access"`
	Op      string `json:"operation,omitempty"`
	Summary string `json:"summary"`
	DryRun  bool   `json:"supports_dry_run"`
}

// agentTools 是 Agent 能调用的接口：管理接口，去掉只给浏览器用的 passkey 与用户侧接口。
func (s *Server) agentTools() []tool {
	var out []tool
	for _, r := range s.routes() {
		if r.Access != accessAdmin && r.Access != accessOperator {
			continue
		}
		summary := r.Summary
		if r.Access == accessOperator {
			summary += "（只接受运营者会话）"
		}
		out = append(out, tool{Method: r.Method, Path: r.Path, Risk: r.Risk, Access: r.Access, Op: r.Op, Summary: summary, DryRun: r.Op != ""})
	}
	return out
}

func (s *Server) skill(c *gin.Context) {
	var buf bytes.Buffer
	err := skillTpl.Execute(&buf, map[string]any{
		"PublicURL":     s.d.Cfg.PublicURL,
		"UserThreshold": s.d.Cfg.Risk.UserThreshold,
		"NodeThreshold": s.d.Cfg.Risk.NodeThreshold,
		"Routes":        s.agentTools(),
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", buf.Bytes())
}

func (s *Server) tools(c *gin.Context) {
	ok(c, gin.H{"tools": s.agentTools(), "risk_thresholds": gin.H{"users": s.d.Cfg.Risk.UserThreshold, "nodes": s.d.Cfg.Risk.NodeThreshold}})
}

func (s *Server) openapi(c *gin.Context) {
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", patatoapi.Spec)
}
