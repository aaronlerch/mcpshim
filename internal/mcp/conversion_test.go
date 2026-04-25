package mcp

import (
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

func TestMetaToMapNil(t *testing.T) {
	if got := metaToMap(nil); got != nil {
		t.Errorf("metaToMap(nil) = %v, want nil", got)
	}
}

func TestMetaToMapEmpty(t *testing.T) {
	got := metaToMap(&mcpproto.Meta{})
	if got != nil {
		t.Errorf("metaToMap(empty) = %v, want nil", got)
	}
}

func TestMetaToMapAdditionalFields(t *testing.T) {
	m := &mcpproto.Meta{
		AdditionalFields: map[string]any{
			"anthropic/maxResultSizeChars": float64(500000),
			"customKey":                    "value",
		},
	}
	got := metaToMap(m)
	if got["anthropic/maxResultSizeChars"] != float64(500000) {
		t.Errorf("missing anthropic/maxResultSizeChars: %v", got)
	}
	if got["customKey"] != "value" {
		t.Errorf("missing customKey: %v", got)
	}
}

func TestMetaToMapWithProgressToken(t *testing.T) {
	m := &mcpproto.Meta{
		ProgressToken: "tok-123",
		AdditionalFields: map[string]any{
			"foo": "bar",
		},
	}
	got := metaToMap(m)
	if got["progressToken"] != mcpproto.ProgressToken("tok-123") {
		t.Errorf("progressToken not preserved: %#v", got["progressToken"])
	}
	if got["foo"] != "bar" {
		t.Errorf("AdditionalFields lost: %#v", got)
	}
}
