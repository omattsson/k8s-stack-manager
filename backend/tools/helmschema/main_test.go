package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const chartDir = "../../../helm/k8s-stack-manager/"

// The committed values.schema.json must be the output for the committed
// values.yaml. Regenerate it with `make helm-schema`.
func TestSchemaIsCurrent(t *testing.T) {
	values, err := os.ReadFile(chartDir + "values.yaml")
	require.NoError(t, err)
	committed, err := os.ReadFile(chartDir + "values.schema.json")
	require.NoError(t, err)

	got, err := generate(values)
	require.NoError(t, err)
	assert.Equal(t, string(committed), string(got), "values.schema.json is out of date: run make helm-schema")
}

func TestSchemaRules(t *testing.T) {
	values, err := os.ReadFile(chartDir + "values.yaml")
	require.NoError(t, err)
	out, err := generate(values)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(out, &schema))
	at := func(path ...string) map[string]any {
		t.Helper()
		node := schema
		for _, p := range path {
			props, ok := node["properties"].(map[string]any)
			require.True(t, ok, "no properties above %s", p)
			node, ok = props[p].(map[string]any)
			require.True(t, ok, "no property %s", p)
		}
		return node
	}

	assert.Equal(t, "http://json-schema.org/draft-07/schema#", schema["$schema"])
	assert.Equal(t, []any{"traefik", "ingress", "none"}, at("ingress", "type")["enum"])
	assert.Equal(t, []any{"namespace", "cluster"}, at("backend", "rbac", "scope")["enum"])
	assert.Equal(t, "boolean", at("backend", "leaderElection", "enabled")["type"])
	assert.Equal(t, "string", at("mysql", "image", "tag")["type"])

	// Values that the templates check themselves get only a type, so the
	// template message stays.
	assert.Equal(t, []any{"integer", "number", "string", "null"}, at("backend", "helmHistoryMax")["type"])
	assert.NotContains(t, at("backend", "helmHistoryMax"), "minimum")
	assert.NotContains(t, at("backend", "maxInstanceTtlDays"), "pattern")
	assert.Equal(t, map[string]any{"type": "string"}, at("backend", "exec", "allowedContainers")["items"])

	// Environment variables are written as text: any scalar, also new keys.
	env := at("backend", "env")
	assert.Equal(t, map[string]any{"type": []any{"string", "number", "boolean"}}, env["additionalProperties"])
	assert.Equal(t, []any{"string", "number", "boolean"}, at("backend", "env", "SERVER_PORT")["type"])

	// The security warnings of the stream features reach the descriptions.
	assert.Contains(t, at("backend", "portForward")["description"], "SECURITY: a tunnel goes past NetworkPolicies")
	assert.Contains(t, at("backend", "exec")["description"], "SECURITY: a command shows the environment of the container")

	// HPA targets: 0 or null leaves the metric out; above 100 is valid.
	for _, c := range []string{"backend", "frontend"} {
		for _, m := range []string{"targetCPUUtilizationPercentage", "targetMemoryUtilizationPercentage"} {
			target := at(c, "autoscaling", m)
			assert.Equal(t, []any{"integer", "null"}, target["type"], c+"."+m)
			assert.Equal(t, float64(0), target["minimum"], c+"."+m)
			assert.NotContains(t, target, "maximum", c+"."+m)
		}
	}

	// No placeholder passwords as defaults.
	assert.NotContains(t, at("backend", "secrets", "JWT_SECRET"), "default")
	assert.NotContains(t, at("mysql", "auth", "rootPassword"), "default")
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name     string
		comments []string
		want     string
	}{
		{"plain", []string{"# Lease name. Empty = \"<fullname>-workers\"."}, "Lease name. Empty = \"<fullname>-workers\"."},
		{"joins lines", []string{"# first line\n# second line"}, "first line second line"},
		{"drops a section banner", []string{"# -----\n# Backend\n# -----\n# The API."}, "The API."},
		{"drops commented-out keys", []string{"#  secretName: k8s-stack-manager-tls"}, ""},
		{"drops an example", []string{"# Extra labels.\n# Example:\n# namespaceLabels:\n#   team: a"}, "Extra labels."},
		{"drops a commented-out key with a value and a comment", []string{"# ACCESS_TOKEN_EXPIRATION: \"15m\"   # lifetime"}, ""},
		{"drops a commented-out flow list", []string{"#   events: [\"deploy-finalized\"]\n# events: [a, b]"}, ""},
		{"keeps a prose heading", []string{"# SECURITY: a tunnel goes past NetworkPolicies."}, "SECURITY: a tunnel goes past NetworkPolicies."},
		{"keeps a two-word heading", []string{"# Note: the rows go to the binary log."}, "Note: the rows go to the binary log."},
		{"line comment", []string{"", "# Database name to create"}, "Database name to create"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, describe(tt.comments...))
		})
	}
}

func TestGenerateErrors(t *testing.T) {
	_, err := generate([]byte(""))
	assert.Error(t, err)
	_, err = generate([]byte("a: [unclosed"))
	assert.Error(t, err)
	_, err = generate([]byte("a:\n  b: !!int abc\n"))
	assert.ErrorContains(t, err, "a: b: line 2")
}

// The rules expect the chart paths, so the edge cases run through the
// building blocks.
func TestScalarUsesYAMLRules(t *testing.T) {
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("[TRUE, 0x10, 1_000, 1.5, null, \"8.4\", text]"), &doc))
	s, err := build(doc.Content[0])
	require.NoError(t, err)
	assert.Equal(t, []any{true, 16, 1000, 1.5, nil, "8.4", "text"}, s.Default)
	assert.Nil(t, s.Items, "mixed item types get no items type")
}

func TestAliasesUseTheReferencedNode(t *testing.T) {
	var doc yaml.Node
	src := "a: &res\n  cpu: 100m\n  sizes: [1, 2]\nb: *res\nc: &n 5\nd: *n\n"
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
	s, err := build(doc.Content[0])
	require.NoError(t, err)
	b := s.Properties.m["b"]
	assert.Equal(t, "object", b.Type)
	assert.Equal(t, "100m", b.Properties.m["cpu"].Default)
	assert.Equal(t, []any{1, 2}, b.Properties.m["sizes"].Default)
	assert.Equal(t, "integer", s.Properties.m["d"].Type)
	assert.Equal(t, 5, s.Properties.m["d"].Default)
}

func TestGenerateRefusesCyclicAliases(t *testing.T) {
	_, err := generate([]byte("a: &x\n  b: *x\n"))
	assert.ErrorContains(t, err, "an alias refers to a node that contains it")
}

func TestQuantityRuleSkipsEmptyMaps(t *testing.T) {
	requests := &Schema{Type: "object"}
	keyRule("backend.resources.requests", requests)
	assert.Equal(t, "object", requests.Type)

	cpu := &Schema{Type: "string"}
	keyRule("backend.resources.requests.cpu", cpu)
	assert.Equal(t, k8sQuantity, cpu.Type)
}
