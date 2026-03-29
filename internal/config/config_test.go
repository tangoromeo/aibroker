package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAuthColonOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	content := `
mode: http
listen: ":0"
upstream:
  url: "http://localhost"
  auth_from_client:
    enabled: true
  client_routing:
    continue:
      colon_bearer_split:
        enabled: true
        id_header: "X-Id"
log:
  level: info
  format: text
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}
