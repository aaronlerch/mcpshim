package client

import (
	"reflect"
	"testing"

	"github.com/mcpshim/mcpshim/internal/protocol"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want interface{}
	}{
		{"true", true},
		{"false", false},
		{"TRUE", true},
		{"1", int64(1)},           // numeric, not boolean
		{"0", int64(0)},           // numeric, not boolean
		{"42", int64(42)},
		{"-5", int64(-5)},
		{"3.14", 3.14},
		{"hello", "hello"},
		{"t", "t"},                // not a bool literal -> string
		{`["a","b"]`, []interface{}{"a", "b"}},
		{`[1,2]`, []interface{}{float64(1), float64(2)}},
		{`{"k":"v"}`, map[string]interface{}{"k": "v"}},
		{"[not json", "[not json"}, // invalid JSON -> string
	}
	for _, c := range cases {
		got := normalize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("normalize(%q) = %#v (%T), want %#v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}

func TestParseDynamicArgsStructured(t *testing.T) {
	args := []string{
		"--tasks", `[{"content":"x","priority":4}]`,
		"--limit", "1",
		"--flag",
	}
	out := parseDynamicArgs(args)

	tasks, ok := out["tasks"].([]interface{})
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks not parsed as array: %#v", out["tasks"])
	}
	if out["limit"] != int64(1) {
		t.Fatalf("limit = %#v, want int64(1)", out["limit"])
	}
	if out["flag"] != true {
		t.Fatalf("valueless flag = %#v, want true", out["flag"])
	}
}

func TestSanitizeAliasName(t *testing.T) {
	cases := map[string]string{
		"notion":         "notion",
		"my-server":      "my_server",
		"my server!!":    "my_server",
		"  __x__  ":      "x",
		"123-notion-api": "s_123_notion_api",
		"!!!":            "",
	}

	for input, want := range cases {
		got := sanitizeAliasName(input)
		if got != want {
			t.Fatalf("sanitizeAliasName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildAliasTargetsDeduplicates(t *testing.T) {
	items := []protocol.ServerInfo{
		{Name: "notion-main", Alias: "notion-main"},
		{Name: "notion_alt", Alias: "notion main"},
		{Name: "notion_3", Alias: "notion_main"},
		{Name: "other", Alias: "!!!"},
	}

	targets := buildAliasTargets(items)
	if len(targets) != 3 {
		t.Fatalf("expected 3 alias targets, got %d", len(targets))
	}

	if targets[0].Sanitized != "notion_main" {
		t.Fatalf("unexpected first alias: %q", targets[0].Sanitized)
	}
	if targets[1].Sanitized != "notion_main_2" {
		t.Fatalf("unexpected second alias: %q", targets[1].Sanitized)
	}
	if targets[2].Sanitized != "notion_main_3" {
		t.Fatalf("unexpected third alias: %q", targets[2].Sanitized)
	}
}
