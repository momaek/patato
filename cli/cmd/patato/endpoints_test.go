package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	patatoapi "github.com/momaek/patato/api"
)

// TestEndpointsInContract 保证 CLI 调用的每个接口都在 api/openapi.yaml 里：服务端改了路径或方法，这里先失败。
func TestEndpointsInContract(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(patatoapi.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	for _, e := range usedEndpoints {
		method, path, _ := strings.Cut(e, " ")
		ops, ok := spec.Paths[path]
		if !ok {
			t.Errorf("%s: path not in openapi.yaml", e)
			continue
		}
		if _, ok := ops[strings.ToLower(method)]; !ok {
			t.Errorf("%s: method not in openapi.yaml", e)
		}
	}
}
