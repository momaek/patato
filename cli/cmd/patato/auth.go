package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	osuser "os/user"
	"runtime"
	"strings"
	"time"

	"github.com/momaek/patato/cli/internal/client"
	"github.com/momaek/patato/cli/internal/config"
)

// 登录用设备授权：CLI 向服务端申请一个短码，运营者在 Admin 上（已用 passkey 登录）核对短码并批准，
// CLI 轮询换到 token。token 只经过 CLI 和服务端之间的直连请求，不出现在任何 URL 里。

func (a *app) auth(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "login":
		return a.authLogin(rest)
	case "status":
		return a.authStatus(rest)
	case "logout":
		return a.authLogout(rest)
	}
	return fmt.Errorf("未知命令 auth %s\n\n%s", sub, usage)
}

type grant struct {
	GrantID         string `json:"grant_id"`
	Secret          string `json:"secret"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	// VerificationPath 是相对 Admin 地址的路径；Admin 挂在隐藏路径下时服务端只返回它。
	VerificationPath string    `json:"verification_path"`
	ExpiresAt        time.Time `json:"expires_at"`
	Interval         int       `json:"interval"`
}

// verificationLink 优先用 --admin-url 拼出授权链接，其次用服务端给的完整链接。
func verificationLink(g grant, adminURL string) string {
	if adminURL = strings.TrimRight(strings.TrimSpace(adminURL), "/"); adminURL != "" && g.VerificationPath != "" {
		return adminURL + g.VerificationPath
	}
	return g.VerificationURL
}

func (a *app) authLogin(args []string) error {
	c := newCommand("auth login", "patato auth login --url <API 地址> [--scope read|write]\n\n在浏览器里授权这台电脑，签发一个 CLI token 写进配置文件。")
	rawURL := c.fs.String("url", os.Getenv("PATATO_URL"), "Patato API 地址，例如 https://api.example.com")
	scope := c.fs.String("scope", "write", "权限：read 只能查询；write 还能预演和执行变更（敏感变更仍需审批）")
	noBrowser := c.fs.Bool("no-browser", false, "不自动打开浏览器，只打印链接")
	adminURL := c.fs.String("admin-url", os.Getenv("PATATO_ADMIN_URL"), "Admin 地址；Admin 挂在隐藏路径下时用来拼出授权链接，例如 https://example.com/a8f3k2c9")
	if err := c.parse(args); err != nil {
		return err
	}
	if *scope != "read" && *scope != "write" {
		return fmt.Errorf("--scope 只能是 read 或 write")
	}
	file, err := config.Read()
	if err != nil {
		return err
	}
	if *rawURL == "" {
		if name, err := file.Resolve(a.profile); err == nil && name != "" {
			*rawURL = file.Profiles[name].URL
		}
	}
	if *rawURL == "" {
		return fmt.Errorf("需要 --url，例如 patato auth login --url https://api.example.com")
	}
	base, err := config.NormalizeURL(*rawURL)
	if err != nil {
		return err
	}
	anon := a.newClient(base, "")
	var g grant
	if _, err := anon.Do(a.ctx, "POST", epGrants, nil, map[string]any{"client_name": clientName(), "scope": *scope}, &g); err != nil {
		return err
	}
	link := verificationLink(g, *adminURL)
	switch {
	case link != "":
		fmt.Fprintf(os.Stderr, "在浏览器里打开下面的链接，确认短码是 %s 后批准：\n\n  %s\n\n", g.UserCode, link)
	case g.VerificationPath != "":
		// Admin 挂在隐藏路径下，服务端不会把地址告诉未登录的调用方
		fmt.Fprintf(os.Stderr, "在浏览器里打开「<Admin 地址>%s」，确认短码是 %s 后批准。\n下次可以加 --admin-url <Admin 地址> 直接打开。\n\n",
			g.VerificationPath, g.UserCode)
	default:
		return fmt.Errorf("服务端没有配置 admin_url，无法生成授权链接；在服务端配置里填写 admin_url")
	}
	if link != "" && !*noBrowser {
		if err := openBrowser(link); err != nil {
			fmt.Fprintf(os.Stderr, "没能自动打开浏览器（%v），请手动打开上面的链接。\n", err)
		}
	}
	fmt.Fprintln(os.Stderr, "等待批准……")
	token, err := a.pollGrant(anon, g)
	if err != nil {
		return err
	}
	name := a.profile
	if name == "" {
		name = file.NameFor(base)
	}
	_, existed := file.Profiles[name]
	file.Profiles[name] = config.Profile{URL: base, Token: token, Scope: *scope}
	if !existed || file.Current == "" {
		file.Current = name
	}
	if err := config.Write(file); err != nil {
		return err
	}
	me := whoami{}
	_, _ = a.newClient(base, token).Do(a.ctx, "GET", epMe, nil, nil, &me)
	a.printf("已登录 %s，身份 %s，权限 %s，profile %q。\ntoken 写入 %s\n", base, me.Operator.Name, scopeText(*scope), name, config.Path())
	return nil
}

func (a *app) pollGrant(anon *client.Client, g grant) (string, error) {
	interval := time.Duration(max(g.Interval, 2)) * time.Second
	for {
		select {
		case <-a.ctx.Done():
			return "", &exitError{code: 130, msg: "已取消登录"}
		case <-time.After(interval):
		}
		var r struct {
			Status string `json:"status"`
			Token  string `json:"token"`
		}
		resp, err := anon.Do(a.ctx, "POST", client.Path(epGrantToken, g.GrantID), nil, map[string]any{"secret": g.Secret}, &r)
		if err != nil {
			if client.IsStatus(err, http.StatusGone) {
				_ = jsonUnmarshal(resp.Body, &r)
				if r.Status == "denied" {
					return "", fmt.Errorf("授权被拒绝")
				}
				return "", fmt.Errorf("授权已过期，重新运行 patato auth login")
			}
			return "", err
		}
		if resp.Status == http.StatusOK && r.Token != "" {
			return r.Token, nil
		}
		if time.Now().After(g.ExpiresAt) {
			return "", fmt.Errorf("授权已过期，重新运行 patato auth login")
		}
	}
}

func (a *app) authStatus(args []string) error {
	c := newCommand("auth status", "patato auth status\n\n显示当前 profile、服务端和 token 是否还能用。")
	if err := c.parse(args); err != nil {
		return err
	}
	name, p, err := config.Load(a.profile)
	if err != nil {
		return err
	}
	a.c = a.newClient(p.URL, p.Token)
	var me whoami
	resp, err := a.c.Do(a.ctx, "GET", epMe, nil, nil, &me)
	if a.json && err == nil {
		printJSON(a.out, resp.Body)
		return nil
	}
	pairs := [][2]string{
		{"Profile", orDash(name)},
		{"服务端", p.URL},
		{"token", maskToken(p.Token)},
		{"配置文件", config.Path()},
	}
	if err != nil {
		kv(a.out, append(pairs, [2]string{"状态", "不可用：" + err.Error()}))
		return err
	}
	kv(a.out, append(pairs,
		[2]string{"身份", fmt.Sprintf("%s（%s）", me.Operator.Name, label(map[string]string{"token": "CLI token", "operator": "运营者会话"}, me.Kind))},
		[2]string{"权限", scopeText(me.Scope)},
		[2]string{"状态", "正常"},
	))
	return nil
}

func (a *app) authLogout(args []string) error {
	c := newCommand("auth logout", "patato auth logout\n\n吊销当前 token 并从配置文件里删掉。")
	if err := c.parse(args); err != nil {
		return err
	}
	file, err := config.Read()
	if err != nil {
		return err
	}
	name, err := file.Resolve(a.profile)
	if err != nil {
		return err
	}
	p, ok := file.Profiles[name]
	if !ok || p.Token == "" {
		return fmt.Errorf("没有已登录的 profile")
	}
	// 先吊销再删除：只删本地副本会留下一个仍然有效的 token
	_, err = a.newClient(p.URL, p.Token).Do(a.ctx, "POST", epLogout, nil, nil, nil)
	if err != nil && !client.IsStatus(err, http.StatusUnauthorized) {
		return fmt.Errorf("吊销 token 失败，本地没有删除：%w", err)
	}
	p.Token = ""
	file.Profiles[name] = p
	if err := config.Write(file); err != nil {
		return err
	}
	a.printf("已退出 profile %q，token 已吊销。\n", name)
	return nil
}

func (a *app) profileCmd(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	file, err := config.Read()
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		current, _ := file.Resolve("")
		rows := [][]string{}
		for _, n := range file.Names() {
			mark := ""
			if n == current {
				mark = "*"
			}
			p := file.Profiles[n]
			st := "已登录"
			if p.Token == "" {
				st = "未登录"
			}
			rows = append(rows, []string{mark, n, p.URL, scopeText(p.Scope), st})
		}
		if len(rows) == 0 {
			a.printf("还没有 profile：运行 patato auth login --url <API 地址>\n")
			return nil
		}
		table(a.out, []string{"", "名字", "服务端", "权限", "状态"}, rows)
		return nil
	case "use":
		if len(rest) != 1 {
			return fmt.Errorf("用法：patato profile use <名字>")
		}
		if _, ok := file.Profiles[rest[0]]; !ok {
			return fmt.Errorf("profile %q 不存在", rest[0])
		}
		file.Current = rest[0]
		if err := config.Write(file); err != nil {
			return err
		}
		a.printf("当前 profile：%s（%s）\n", rest[0], file.Profiles[rest[0]].URL)
		return nil
	case "remove", "rm":
		if len(rest) != 1 {
			return fmt.Errorf("用法：patato profile remove <名字>")
		}
		p, ok := file.Profiles[rest[0]]
		if !ok {
			return fmt.Errorf("profile %q 不存在", rest[0])
		}
		if p.Token != "" {
			return fmt.Errorf("profile %q 还有 token：先运行 patato auth logout --profile %s 吊销它", rest[0], rest[0])
		}
		delete(file.Profiles, rest[0])
		if file.Current == rest[0] {
			file.Current = ""
		}
		if err := config.Write(file); err != nil {
			return err
		}
		a.printf("已删除 profile %s\n", rest[0])
		return nil
	}
	return fmt.Errorf("未知命令 profile %s\n\n%s", sub, usage)
}

func scopeText(s string) string {
	return label(map[string]string{"read": "只读", "write": "可写", "": "—"}, s)
}

func maskToken(t string) string {
	if len(t) <= 10 {
		return "***"
	}
	return t[:6] + "…" + t[len(t)-4:]
}

// clientName 是授权页上显示的客户端名：哪台电脑上的哪个用户。
func clientName() string {
	host, _ := os.Hostname()
	who := ""
	if u, err := osuser.Current(); err == nil {
		who = u.Username
	}
	parts := []string{"patato CLI"}
	if who != "" || host != "" {
		parts = append(parts, strings.Trim(who+"@"+host, "@"))
	}
	return strings.Join(parts, " · ")
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
