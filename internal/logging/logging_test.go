package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestRawHTTPDiagnosticsCannotLeakSecretsOrSpoofLogs(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out, slog.LevelDebug)
	HTTPErrorLog(logger).Print("request Authorization: Bearer PRIVATE_KEY\n{\"msg\":\"forged\"}")
	if strings.Contains(out.String(), "PRIVATE_KEY") || strings.Contains(out.String(), "forged") {
		t.Fatal("raw HTTP diagnostic leaked into lifecycle log")
	}
	var entry map[string]any
	if err := json.Unmarshal(out.Bytes(), &entry); err != nil || entry["msg"] != "http_connection_error" {
		t.Fatalf("expected one structured safe event: %s", out.String())
	}
	out.Reset()
	HTTPErrorLog(New(&out, slog.LevelInfo)).Print("PRIVATE_KEY")
	if out.Len() != 0 {
		t.Fatal("debug diagnostic ignored configured level")
	}
}
