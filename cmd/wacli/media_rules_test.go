package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runRulesCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newMediaRulesCmd(&rootFlags{})
	cmd.SetArgs(args)
	var err error
	stdout := captureRootStdout(t, func() { err = cmd.Execute() })
	return stdout, err
}

func TestMediaRulesCheckAndTest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rules.json")
	js := `{"version":1,"rules":[
		{"name":"pdfs","match":{"kind":"dm"},"types":["document"],"extensions":["pdf"],"max_bytes":100},
		{"name":"fam","match":{"list":"Family"},"types":["image"]},
		{"name":"no groups","match":{"kind":"group"},"download":false}]}`
	if err := os.WriteFile(p, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runRulesCmd(t, "check", p)
	if err != nil || !strings.Contains(out, "OK: 3 rule(s)") || !strings.Contains(out, "no groups: skip") {
		t.Fatalf("check: %q %v", out, err)
	}
	out, err = runRulesCmd(t, "test", p, "--chat", "1@s.whatsapp.net", "--type", "document", "--ext", "pdf", "--size", "50")
	if err != nil || !strings.Contains(out, "download: matched rule pdfs") {
		t.Fatalf("test: %q %v", out, err)
	}
	out, _ = runRulesCmd(t, "test", p, "--chat", "1@s.whatsapp.net", "--type", "document", "--ext", "pdf", "--size", "500")
	if !strings.Contains(out, "no download") {
		t.Fatalf("too big: %q", out)
	}
	out, _ = runRulesCmd(t, "test", p, "--chat", "1@s.whatsapp.net", "--type", "image", "--in-list", "Family")
	if !strings.Contains(out, "matched rule fam") {
		t.Fatalf("list: %q", out)
	}
	out, _ = runRulesCmd(t, "test", p, "--chat", "2@g.us", "--type", "image")
	if !strings.Contains(out, "excluded by rule no groups") {
		t.Fatalf("group: %q", out)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"version":1,"rules":[{"match":{},"types":["voice"]}]}`), 0o600)
	if _, err := runRulesCmd(t, "check", bad); err == nil {
		t.Fatal("expected invalid rules error")
	}
}
