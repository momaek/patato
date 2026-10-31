// Package config 读写 patato CLI 的配置：一台电脑上可以有多个 profile，各自指向一个 Patato 部署。
//
// 文件默认在 ~/.config/patato/config.yaml（$PATATO_CONFIG 或 $XDG_CONFIG_HOME 可以改），权限 0600。
// 选哪个 profile：--profile，其次 $PATATO_PROFILE，再次文件里的 current，只有一个时就用它。
// 没有指名 profile 时，$PATATO_URL 和 $PATATO_TOKEN 可以覆盖对应字段，便于在 CI 或临时环境里使用。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile 是连接一个部署需要的全部信息。
type Profile struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
	// Scope 是登录时选的权限（read / write），只用于展示；真正的权限由服务端按 token 判断。
	Scope string `yaml:"scope,omitempty"`
}

type File struct {
	Current  string             `yaml:"current,omitempty"`
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
}

const DefaultProfile = "default"

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("profile 名 %q 只能包含字母、数字、点、下划线和连字符", name)
	}
	return nil
}

// Path 返回配置文件的位置。
func Path() string {
	if p := os.Getenv("PATATO_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "patato", "config.yaml")
}

// Read 读取配置文件；文件不存在时返回空配置。
func Read() (*File, error) {
	f := &File{Profiles: map[string]Profile{}}
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("配置文件 %s 格式不对：%w", Path(), err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	return f, nil
}

// Write 原子地写回配置文件，权限 0600：里面有 token。
func Write(f *File) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Names 按字母序返回全部 profile 名。
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Resolve 决定用哪个 profile。指名的 profile 不存在时报错，不悄悄换成别的部署。
func (f *File) Resolve(explicit string) (string, error) {
	for _, a := range []struct{ name, source string }{
		{strings.TrimSpace(explicit), "--profile"},
		{strings.TrimSpace(os.Getenv("PATATO_PROFILE")), "$PATATO_PROFILE"},
	} {
		if a.name == "" {
			continue
		}
		if _, ok := f.Profiles[a.name]; !ok {
			return "", fmt.Errorf("%s %q 不存在%s", a.source, a.name, f.available())
		}
		return a.name, nil
	}
	if _, ok := f.Profiles[f.Current]; ok && f.Current != "" {
		return f.Current, nil
	}
	if len(f.Profiles) == 1 {
		return f.Names()[0], nil
	}
	if len(f.Profiles) > 1 {
		return "", fmt.Errorf("有多个 profile，没有指定用哪个%s；用 --profile 或 patato profile use <名字>", f.available())
	}
	return "", nil
}

func (f *File) available() string {
	if len(f.Profiles) == 0 {
		return ""
	}
	return "（现有：" + strings.Join(f.Names(), "、") + "）"
}

// Load 返回要用的 profile 名和连接信息。
func Load(explicit string) (string, Profile, error) {
	f, err := Read()
	if err != nil {
		return "", Profile{}, err
	}
	name, err := f.Resolve(explicit)
	if err != nil {
		return "", Profile{}, err
	}
	p := f.Profiles[name]
	// 指名了 profile 时不让环境变量覆盖：过期的 PATATO_URL 不该把命令悄悄打到别的部署上
	if strings.TrimSpace(explicit) == "" {
		if v := strings.TrimSpace(os.Getenv("PATATO_URL")); v != "" {
			p.URL = v
		}
		if v := strings.TrimSpace(os.Getenv("PATATO_TOKEN")); v != "" {
			p.Token = v
		}
	}
	if p.URL == "" || p.Token == "" {
		return name, p, fmt.Errorf("还没有登录：运行 patato auth login --url <API 地址>，或设置 PATATO_URL 和 PATATO_TOKEN（配置文件：%s）", Path())
	}
	p.URL = strings.TrimRight(p.URL, "/")
	return name, p, nil
}

// NormalizeURL 校验并规整 API 地址：必须是 http(s)，去掉末尾的斜杠。
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("API 地址 %q 不对，应该像 https://api.example.com", raw)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// NameFor 给新登录的部署挑一个 profile 名：同一地址已有 profile 就复用，没有任何 profile 时叫 default，
// 否则用域名。
func (f *File) NameFor(base string) string {
	for _, n := range f.Names() {
		if strings.TrimRight(f.Profiles[n].URL, "/") == base {
			return n
		}
	}
	if len(f.Profiles) == 0 {
		return DefaultProfile
	}
	u, _ := url.Parse(base)
	name := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(u.Hostname(), "-")
	if ValidateName(name) != nil {
		return DefaultProfile
	}
	return name
}
