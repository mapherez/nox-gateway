package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func TestDefaultsAndExplicitConfiguration(t *testing.T) {
	c, err := Load(lookup(nil))
	if err != nil || c.ListenAddr != "127.0.0.1:8080" || !filepath.IsAbs(c.DataDir) || c.LogLevel != slog.LevelInfo || c.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: %#v, %v", c, err)
	}
	c, err = Load(lookup(map[string]string{
		"NOX_GATEWAY_LISTEN_ADDR": "[::1]:0", "NOX_GATEWAY_DATA_DIR": t.TempDir(),
		"NOX_GATEWAY_LOG_LEVEL": "debug", "NOX_GATEWAY_SHUTDOWN_TIMEOUT_MS": "1250",
	}))
	if err != nil || c.ListenAddr != "[::1]:0" || c.LogLevel != slog.LevelDebug || c.ShutdownTimeout != 1250*time.Millisecond {
		t.Fatalf("explicit settings not applied: %#v, %v", c, err)
	}
	for _, addr := range []string{"localhost:8080", "0.0.0.0:8080", ":8080"} {
		if _, err := Load(lookup(map[string]string{"NOX_GATEWAY_LISTEN_ADDR": addr})); err != nil {
			t.Fatalf("valid bind rejected: %s", addr)
		}
	}
}

func TestInvalidSettingsNeverEchoValues(t *testing.T) {
	cases := map[string][]string{
		"NOX_GATEWAY_LISTEN_ADDR":         {"", "secret.example:8080", "127.0.0.1", "https://secret.example:8443", "127.0.0.1:65536", "127.0.0.1:-1", "127.0.0.1:+1", "127.0.0.1:080", "127.0.0.1:http", "[fe80::1%en0]:80", " 127.0.0.1:80", "127.0.0.1:"},
		"NOX_GATEWAY_DATA_DIR":            {"", " ", "path\x00secret", "path\nsecret"},
		"NOX_GATEWAY_LOG_LEVEL":           {"", "trace-secret", "INFO"},
		"NOX_GATEWAY_SHUTDOWN_TIMEOUT_MS": {"", "0", "-1", "60001", "9999999999999999999999", "secret"},
	}
	for name, values := range cases {
		for i, value := range values {
			t.Run(name+"/"+string(rune('a'+i)), func(t *testing.T) {
				_, err := Load(lookup(map[string]string{name: value}))
				if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "secret") {
					t.Fatalf("expected safe setting-specific error: %v", err)
				}
			})
		}
	}
}

func TestSameHostConfigurationHasNoCertificateOrNetworkPrerequisites(t *testing.T) {
	c, e := Load(lookup(map[string]string{"NOX_GATEWAY_PUBLIC_URL": "http://gateway.home.arpa/", "NOX_GATEWAY_SUITE_SOCKET_DIR": t.TempDir()}))
	if e != nil || c.PublicURL != "http://gateway.home.arpa" || c.BackendURL != "http://nox-backend" {
		t.Fatal("local configuration rejected", e)
	}
	for _, v := range []string{"relative/path", "/tmp/secret\n"} {
		if _, e := Load(lookup(map[string]string{"NOX_GATEWAY_SUITE_SOCKET_DIR": v})); e == nil {
			t.Fatal("invalid local directory accepted")
		}
	}
	if _, e := Load(lookup(map[string]string{"NOX_GATEWAY_TLS_CERT_FILE": "cert.pem"})); e == nil {
		t.Fatal("half native TLS configuration accepted")
	}
}

func TestDeploymentLimits(t *testing.T) {
	c, e := Load(lookup(map[string]string{"NOX_GATEWAY_CALL_TIMEOUT_MS": "120000", "NOX_GATEWAY_MAX_CONCURRENT": "4", "NOX_GATEWAY_PROBE_INTERVAL_MS": "30000", "NOX_GATEWAY_MAX_PAYLOAD_BYTES": "1024"}))
	if e != nil || c.CallTimeout != 2*time.Minute || c.MaxConcurrent != 4 || c.ProbeInterval != 30*time.Second || c.MaxPayloadBytes != 1024 {
		t.Fatal("limits not applied", e)
	}
	for key, value := range map[string]string{"NOX_GATEWAY_CALL_TIMEOUT_MS": "0", "NOX_GATEWAY_MAX_CONCURRENT": "257", "NOX_GATEWAY_PROBE_INTERVAL_MS": "0", "NOX_GATEWAY_MAX_PAYLOAD_BYTES": "2097153"} {
		if _, e := Load(lookup(map[string]string{key: value})); e == nil {
			t.Fatal("invalid limit accepted", key)
		}
	}
}
