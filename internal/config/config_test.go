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

func TestSuiteTLSConfigurationRejectsImplicitTrustAndInvalidOrigins(t *testing.T) {
	valid := map[string]string{"NOX_GATEWAY_PUBLIC_URL": "https://gateway.home.arpa/", "NOX_GATEWAY_BACKEND_URL": "https://backend.home.arpa/", "NOX_GATEWAY_TLS_CERT_FILE": "cert.pem", "NOX_GATEWAY_TLS_KEY_FILE": "key.pem", "NOX_GATEWAY_SUITE_ALLOWED_CIDRS": "192.168.10.0/24"}
	c, err := Load(lookup(valid))
	if err != nil || c.PublicURL != "https://gateway.home.arpa" || c.BackendURL != "https://backend.home.arpa" {
		t.Fatal("valid configured origins did not normalize")
	}
	for name, values := range map[string][]string{
		"NOX_GATEWAY_PUBLIC_URL":          {"http://gateway.home.arpa", "https://gateway.home.arpa/apps/gateway", "https://secret@gateway.home.arpa", ""},
		"NOX_GATEWAY_SUITE_ALLOWED_CIDRS": {"", "192.168.10.10/24", "secret"},
		"NOX_GATEWAY_SUITE_ALLOWED_PORTS": {"", "0", "65536", "0443", "-1"},
		"NOX_GATEWAY_TLS_KEY_FILE":        {""}, "NOX_GATEWAY_BACKEND_TLS_PIN": {"secret"},
	} {
		for _, value := range values {
			next := map[string]string{}
			for k, v := range valid {
				next[k] = v
			}
			next[name] = value
			if _, err := Load(lookup(next)); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe Suite configuration accepted for %s", name)
			}
		}
	}
	delete(valid, "NOX_GATEWAY_TLS_CERT_FILE")
	delete(valid, "NOX_GATEWAY_TLS_KEY_FILE")
	valid["NOX_GATEWAY_PUBLIC_TLS_CERT_FILE"] = "npm-cert.pem"
	if _, err := Load(lookup(valid)); err == nil {
		t.Fatal("proxy certificate alone granted transport trust")
	}
	valid["NOX_GATEWAY_TRUSTED_PROXY_CIDRS"] = "172.19.0.2/32"
	if _, err := Load(lookup(valid)); err != nil {
		t.Fatal(err)
	}
}
