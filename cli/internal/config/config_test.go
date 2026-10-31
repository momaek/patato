package config

import (
	"path/filepath"
	"testing"
)

func TestResolveAndEnv(t *testing.T) {
	t.Setenv("PATATO_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("PATATO_PROFILE", "")
	t.Setenv("PATATO_URL", "")
	t.Setenv("PATATO_TOKEN", "")
	if _, _, err := Load(""); err == nil {
		t.Fatal("no config should fail with a login hint")
	}
	f, _ := Read()
	f.Profiles["work"] = Profile{URL: "https://api.work.example", Token: "pat_work"}
	f.Profiles["home"] = Profile{URL: "https://api.home.example", Token: "pat_home"}
	if err := Write(f); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(""); err == nil {
		t.Fatal("two profiles and no current must not guess")
	}
	if _, _, err := Load("nope"); err == nil {
		t.Fatal("unknown explicit profile must fail")
	}
	f.Current = "work"
	_ = Write(f)
	name, p, err := Load("")
	if err != nil || name != "work" || p.Token != "pat_work" {
		t.Fatalf("current: %s %+v %v", name, p, err)
	}
	t.Setenv("PATATO_URL", "https://override.example")
	if _, p, _ := Load(""); p.URL != "https://override.example" {
		t.Fatal("env overrides when no profile is named")
	}
	if _, p, _ := Load("home"); p.URL != "https://api.home.example" {
		t.Fatal("env must not override an explicitly named profile")
	}
}

func TestNameForAndURL(t *testing.T) {
	f := &File{Profiles: map[string]Profile{}}
	if f.NameFor("https://api.example.com") != DefaultProfile {
		t.Fatal("first profile is default")
	}
	f.Profiles["default"] = Profile{URL: "https://api.example.com"}
	if f.NameFor("https://api.example.com") != "default" {
		t.Fatal("same URL reuses profile")
	}
	if f.NameFor("https://api.other.io") != "api.other.io" {
		t.Fatal(f.NameFor("https://api.other.io"))
	}
	if u, err := NormalizeURL("api.example.com/"); err != nil || u != "https://api.example.com" {
		t.Fatalf("%s %v", u, err)
	}
	if _, err := NormalizeURL("ftp://x"); err == nil {
		t.Fatal("ftp should fail")
	}
}
