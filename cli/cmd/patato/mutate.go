package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/momaek/patato/cli/internal/client"
)

// mutate 是所有写命令的出口：带上 dry_run 和 instruction 调用写接口，按结果输出。
//
//   - --dry-run：输出变更计划和 change_id，什么都不改。
//   - 常规操作：直接执行，输出结果。
//   - 敏感操作（或因影响面升为敏感）：服务端返回 202 和审批 ID；加 --wait 时等到审批有结果。
func (a *app) mutate(method, path string, body map[string]any, w *writeFlags) error {
	if body == nil {
		body = map[string]any{}
	}
	if w.dryRun {
		body["dry_run"] = true
	}
	if strings.TrimSpace(w.instruction) != "" {
		body["instruction"] = strings.TrimSpace(w.instruction)
	}
	var p changePlan
	resp, err := a.c.Do(a.ctx, method, path, nil, body, &p)
	if err != nil {
		return err
	}
	return a.afterChange(resp, &p, w)
}

// afterChange 处理一个变更计划的返回：输出它，需要时等待审批。
func (a *app) afterChange(resp *client.Response, p *changePlan, w *writeFlags) error {
	waiting := resp.Status == http.StatusAccepted && w.wait
	if a.json && !waiting {
		printJSON(a.out, resp.Body)
		return outcome(p.Status)
	}
	if !a.json {
		a.renderChange(p, !waiting)
	}
	if !waiting {
		return outcome(p.Status)
	}
	d, raw, err := a.waitApprovalRaw(p.ApprovalID, w.timeout)
	if err != nil {
		return err
	}
	if a.json {
		printJSON(a.out, raw)
	} else {
		a.printf("\n")
		a.renderDecision(d)
	}
	return decisionOutcome(d)
}

// outcome 把计划状态换成退出码：作废、过期、失败返回 3，便于脚本和 Agent 判断。
func outcome(status string) error {
	switch status {
	case "stale", "expired", "failed", "rejected":
		return &exitError{code: 3}
	}
	return nil
}

func decisionOutcome(d *approvalDetail) error {
	if d.Status == "approved" && (d.Change == nil || d.Change.Status == "applied") {
		return nil
	}
	return &exitError{code: 3}
}

// renderChange 输出变更计划；hints 为 false 时不打印「下一步该运行什么」的提示（例如正在等待审批）。
func (a *app) renderChange(p *changePlan, hints bool) {
	switch p.Status {
	case "planned":
		a.printf("预演：%s\n", p.Summary)
	case "applied":
		a.printf("已执行：%s\n", p.Summary)
	case "pending_approval":
		a.printf("需要审批：%s\n", p.Summary)
	case "stale":
		a.printf("已作废：%s\n", p.Summary)
	case "expired":
		a.printf("已过期：%s\n", p.Summary)
	case "failed":
		a.printf("执行失败：%s\n", p.Summary)
	default:
		a.printf("%s：%s\n", p.Status, p.Summary)
	}
	risk := label(riskNames, p.Risk)
	if p.DeclaredRisk != "" && p.DeclaredRisk != p.Risk {
		risk += fmt.Sprintf("（接口声明为%s）", label(riskNames, p.DeclaredRisk))
	}
	if p.RiskReason != "" {
		risk += "，" + p.RiskReason
	}
	a.printf("\n")
	kv(a.out, [][2]string{
		{"风险", risk},
		{"影响", fmt.Sprintf("%d 名用户，%d 个节点", p.Impact.Users, p.Impact.Nodes)},
	})
	if p.Status == "planned" || p.Status == "pending_approval" {
		if len(p.Steps) > 0 {
			a.printf("\n步骤\n")
			for i, s := range p.Steps {
				a.printf("  %d. %s\n", i+1, s)
			}
		}
		if len(p.Diff) > 0 {
			a.printf("\n变更\n")
			for _, d := range p.Diff {
				if d.Field == "prices" {
					a.printf("  价格（元）：%s → %s\n", pricesText(centsMap(d.Before)), pricesText(centsMap(d.After)))
					continue
				}
				a.printf("  %s：%s → %s\n", d.Field, valueText(d.Before), valueText(d.After))
			}
		}
	}
	if p.Error != "" {
		a.printf("\n原因：%s\n", p.Error)
	}
	if len(p.Result) > 0 && string(p.Result) != "null" && p.Status == "applied" {
		a.renderResult(p.Result)
	}
	if !hints {
		return
	}
	a.printf("\n")
	switch p.Status {
	case "planned":
		a.printf("change_id %s，%s失效\n", p.ChangeID, untilText(p.ExpiresAt))
		if p.Risk == "sensitive" {
			a.printf("这是敏感操作：执行后会生成审批，需要运营者用 passkey 或 Telegram 批准。\n")
		}
		a.printf("执行：patato change apply %s\n", p.ChangeID)
	case "pending_approval":
		a.printf("审批 %s 已推送到 Telegram 和 Admin，%s过期。\n", p.ApprovalID, untilText(p.ExpiresAt))
		a.printf("在这台电脑上批准：patato approval approve %s\n", p.ApprovalID)
		a.printf("等待结果：patato approval show %s --wait\n", p.ApprovalID)
	case "applied":
		a.printf("change_id %s\n", p.ChangeID)
	}
}

// centsMap 把变更计划里的价格对象（周期 → 分）取出来。
func centsMap(v any) map[string]int64 {
	m, _ := v.(map[string]any)
	out := map[string]int64{}
	for k, x := range m {
		if f, ok := x.(float64); ok {
			out[k] = int64(f)
		}
	}
	return out
}

// renderResult 输出执行结果里的字段，「next」单独一行作为下一步提示。
func (a *app) renderResult(raw json.RawMessage) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	next, _ := m["next"].(string)
	delete(m, "next")
	pairs := [][2]string{}
	for _, k := range sortedKeys(m) {
		pairs = append(pairs, [2]string{k, valueText(m[k])})
	}
	if len(pairs) > 0 {
		a.printf("\n")
		kv(a.out, pairs)
	}
	if next != "" {
		a.printf("\n下一步：%s\n", next)
	}
}

// waitApproval 轮询审批直到有结果。审批由人决定，CLI 只是等；Ctrl-C 退出不影响审批本身。
func (a *app) waitApproval(id string, timeout time.Duration) (*approvalDetail, error) {
	if id == "" {
		return nil, fmt.Errorf("服务端没有返回审批 ID")
	}
	deadline := time.Now().Add(timeout)
	fmt.Fprintf(os.Stderr, "等待审批 %s 的结果……（Ctrl-C 退出不影响审批）\n", id)
	for {
		var d approvalDetail
		if _, err := a.c.Do(a.ctx, "GET", client.Path(epApproval, id), nil, nil, &d); err != nil {
			return nil, err
		}
		if d.Status != "pending" {
			return &d, nil
		}
		if time.Now().After(d.ExpiresAt) {
			d.Status = "expired"
			return &d, nil
		}
		if time.Now().After(deadline) {
			return nil, &exitError{code: 1, msg: fmt.Sprintf("等了 %s 还没有结果；审批仍然有效，之后用 patato approval show %s 查看", timeout, id)}
		}
		select {
		case <-a.ctx.Done():
			return nil, &exitError{code: 130, msg: "已停止等待；审批仍然有效"}
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *app) renderDecision(d *approvalDetail) {
	summary := ""
	if d.Change != nil {
		summary = d.Change.Summary
	}
	switch d.Status {
	case "approved":
		a.printf("已批准：%s\n", summary)
	case "rejected":
		a.printf("已拒绝：%s\n", summary)
	case "expired":
		a.printf("审批已过期，变更没有执行：%s\n", summary)
	default:
		a.printf("%s：%s\n", label(approvalNames, d.Status), summary)
	}
	pairs := [][2]string{}
	if d.DecidedBy != "" {
		pairs = append(pairs, [2]string{"决定人", fmt.Sprintf("%s，经%s", d.DecidedBy, label(entryNames, d.DecidedVia))})
	}
	if d.Reason != "" {
		pairs = append(pairs, [2]string{"原因", d.Reason})
	}
	if d.Change != nil {
		pairs = append(pairs, [2]string{"变更计划", fmt.Sprintf("%s（%s）", d.Change.ChangeID, d.Change.Status)})
		if d.Change.Error != "" {
			pairs = append(pairs, [2]string{"执行错误", d.Change.Error})
		}
	}
	if len(pairs) > 0 {
		kv(a.out, pairs)
	}
	if d.Change != nil && d.Change.Status == "applied" {
		a.renderResult(d.Change.Result)
	}
}
