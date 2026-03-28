package broker

import (
	"strings"
	"testing"
)

func TestSanitizeText_RedactsSecretsAndHosts(t *testing.T) {
	// Synthetic patterns only (not real credentials).
	in := `
upstream:
  url: "http://svc.example-internal.croc.ru"
  api_key: "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5eg=="
headers:
  ContinueDevProject: "0123456789abcdef01234567"
`
	var n int
	out := sanitizeText(in, &n)
	if n == 0 {
		t.Fatal("expected some redactions")
	}
	if strings.Contains(out, "croc.ru") {
		t.Error("corp host should be redacted")
	}
	if strings.Contains(out, "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5eg==") {
		t.Error("api key should be redacted")
	}
	if strings.Contains(out, "0123456789abcdef01234567") {
		t.Error("project id should be redacted")
	}
}

func TestSanitizeText_PreservesEnvSyntax(t *testing.T) {
	in := `key: "${UPSTREAM_API_KEY}"`
	var n int
	out := sanitizeText(in, &n)
	if !strings.Contains(out, "${UPSTREAM_API_KEY}") {
		t.Error("env placeholder should be preserved", out)
	}
}
