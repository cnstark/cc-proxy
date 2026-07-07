package config

import "testing"

func TestParseUpstreamModel(t *testing.T) {
	cases := []struct {
		in       string
		up, model string
		ok       bool
	}{
		{"anthropic/claude-opus-4-8", "anthropic", "claude-opus-4-8", true},
		{"vendor/a/b/c", "vendor", "a/b/c", true}, // model 含 / 原样保留
		{"cfg1", "", "", false},            // 无 /
		{"/model", "", "", false},          // upstream 为空
		{"cfg1/", "", "", false},           // model 为空
		{"", "", "", false},
	}
	for _, c := range cases {
		up, model, ok := ParseUpstreamModel(c.in)
		if up != c.up || model != c.model || ok != c.ok {
			t.Errorf("ParseUpstreamModel(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, up, model, ok, c.up, c.model, c.ok)
		}
	}
}
