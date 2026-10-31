package api

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	patatoapi "github.com/momaek/patato/api"
)

// TestRoutesMatchContract 保证路由表与 api/openapi.yaml 一致：
// 每条路由都在契约里，方法、x-risk、x-access、x-operation 相同；契约里也没有多余的接口。
func TestRoutesMatchContract(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			Risk   string `yaml:"x-risk"`
			Access string `yaml:"x-access"`
			Op     string `yaml:"x-operation"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(patatoapi.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	seen := map[string]bool{}
	for _, r := range s.routes() {
		key := r.Method + " " + r.Path
		if seen[key] {
			t.Errorf("duplicate route %s", key)
		}
		seen[key] = true
		ops, ok := spec.Paths[r.Path]
		if !ok {
			t.Errorf("%s missing from openapi.yaml", key)
			continue
		}
		op, ok := ops[strings.ToLower(r.Method)]
		if !ok {
			t.Errorf("%s missing from openapi.yaml", key)
			continue
		}
		if op.Risk != r.Risk {
			t.Errorf("%s: x-risk %q in contract, %q in routes", key, op.Risk, r.Risk)
		}
		if op.Access != r.Access {
			t.Errorf("%s: x-access %q in contract, %q in routes", key, op.Access, r.Access)
		}
		if op.Op != r.Op {
			t.Errorf("%s: x-operation %q in contract, %q in routes", key, op.Op, r.Op)
		}
	}
	for path, ops := range spec.Paths {
		for method := range ops {
			if !seen[strings.ToUpper(method)+" "+path] {
				t.Errorf("%s %s is in openapi.yaml but not implemented", strings.ToUpper(method), path)
			}
		}
	}
}

func TestInternalPath(t *testing.T) {
	cases := map[string]string{
		"/api/v1/nodes:provision":             "/api/v1/nodes/~provision",
		"/api/v1/nodes/{id}:attach":           "/api/v1/nodes/:id/~attach",
		"/api/v1/nodes/{id}":                  "/api/v1/nodes/:id",
		"/api/v1/me/subscription:reset-token": "/api/v1/me/subscription/~reset-token",
	}
	for in, want := range cases {
		if got := internalPath(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	if p, ok := rewriteVerb("/api/v1/nodes/12:attach"); !ok || p != "/api/v1/nodes/12/~attach" {
		t.Errorf("rewrite: %s", p)
	}
	if _, ok := rewriteVerb("/api/v1/nodes/12"); ok {
		t.Error("plain path must not be rewritten")
	}
}

func TestRedactHidesSecrets(t *testing.T) {
	m := redact([]byte(`{"reality_settings":{"private_key":"PRIV","public_key":"PUB"},"server_key":"SK","obfs":{"password":"x"}}`))
	rs := m["reality_settings"].(map[string]any)
	if rs["private_key"] != "***" || rs["public_key"] != "PUB" || m["server_key"] != "***" || m["obfs"].(map[string]any)["password"] != "***" {
		t.Fatalf("redact: %v", m)
	}
}
