package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSyncCommandExposesWebhookFlags(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	for _, name := range []string{"webhook", "webhook-secret", "webhook-secret-file", "webhook-allow-private", "webhook-events"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestSyncCommandWebhookEventsDefaultsToMessage(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	flag := cmd.Flags().Lookup("webhook-events")
	if flag == nil {
		t.Fatal("missing --webhook-events flag")
	}
	if flag.DefValue != "message" {
		t.Fatalf("--webhook-events default = %q, want \"message\"", flag.DefValue)
	}
}

func TestSyncCommandRejectsUnknownWebhookEvent(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook", "https://example.test/hook", "--webhook-events", "message,presence"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-events must be a comma-separated list of") {
		t.Fatalf("expected webhook-events validation error, got %v", err)
	}
}

func TestSyncCommandRequiresWebhookForEvents(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook-events", "receipt"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-events requires --webhook") {
		t.Fatalf("expected webhook-events validation error, got %v", err)
	}
}

func TestSyncCommandRequiresWebhookForSecret(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook-secret", "secret"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-secret requires --webhook") {
		t.Fatalf("expected webhook-secret validation error, got %v", err)
	}
}

func TestSyncCommandRejectsIneffectiveStaleThreshold(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--stale-threshold", "2m20s"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--stale-threshold must be less than 2m20s") {
		t.Fatalf("expected stale-threshold validation error, got %v", err)
	}
}

func TestSyncCommandRejectsInvalidPresenceMode(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--presence-mode", "loud"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--presence-mode must be one of: normal, quiet") {
		t.Fatalf("expected presence-mode validation error, got %v", err)
	}
}

func writeSecretFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSyncCommandRejectsWebhookSecretWithFile(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook", "https://example.test/hook", "--webhook-secret", "a", "--webhook-secret-file", writeSecretFile(t, "b\n", 0o600)})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually exclusive error, got %v", err)
	}
}

func TestSyncCommandRequiresWebhookForSecretFile(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook-secret-file", writeSecretFile(t, "secret\n", 0o600)})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-secret-file requires --webhook") {
		t.Fatalf("expected webhook-secret-file validation error, got %v", err)
	}
}

func TestSyncCommandRejectsBadWebhookSecretFile(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"missing", filepath.Join(t.TempDir(), "nope"), "no such file"},
		{"empty", writeSecretFile(t, " \n\t", 0o600), "is empty"},
		{"loose permissions", writeSecretFile(t, "topsecret\n", 0o644), "chmod 600"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "loose permissions" && runtime.GOOS == "windows" {
				t.Skip("POSIX permissions")
			}
			cmd := newSyncCmd(&rootFlags{})
			cmd.SetArgs([]string{"--webhook", "https://example.test/hook", "--webhook-secret-file", tt.path})

			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
			if strings.Contains(err.Error(), "topsecret") {
				t.Fatalf("error leaks secret: %v", err)
			}
		})
	}
}

func TestReadWebhookSecretFileTrimsSecret(t *testing.T) {
	got, err := readWebhookSecretFile(writeSecretFile(t, "  s3cret\n", 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Fatalf("secret = %q, want %q", got, "s3cret")
	}
}
