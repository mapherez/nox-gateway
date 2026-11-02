// Package acceptance validates actual executables, not a product substitute.
package acceptance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mapherez/nox-gateway/internal/testutil"
)

type process struct {
	cmd  *exec.Cmd
	done chan error
	logs *os.File
}

func start(t *testing.T, binary string, env []string) *process {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "process-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Env = env
	cmd.Stdout = file
	cmd.Stderr = file
	if err = cmd.Start(); err != nil {
		file.Close()
		t.Fatal("could not start integration executable")
	}
	p := &process{cmd: cmd, done: make(chan error, 1), logs: file}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() { p.stop(t) })
	return p
}
func (p *process) stop(t *testing.T) {
	t.Helper()
	if p.cmd == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Error("integration process required forced shutdown")
	}
	p.logs.Close()
	p.cmd = nil
}
func port(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	return address
}
func cleanEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "NOX_") {
			env = append(env, entry)
		}
	}
	return env
}
func wait(t *testing.T, client *http.Client, url string, want func(int, []byte) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		request, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		response, err := client.Do(request)
		if err == nil {
			b, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if want(response.StatusCode, b) {
				return
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(40 * time.Millisecond):
		}
	}
	t.Fatal("integration service did not reach its expected state before the deadline")
}
func request(t *testing.T, client *http.Client, method, url, token, key, revision string, body any) (int, []byte) {
	t.Helper()
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if revision != "" {
		req.Header.Set("If-Match", `"`+revision+`"`)
	} else if method == "POST" && strings.HasSuffix(url, "/pairings") {
		req.Header.Set("If-None-Match", "*")
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("integration HTTP request failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func TestActualBackendAndGatewayPairRotateRevokeAndRestart(t *testing.T) {
	backendBinary := os.Getenv("NOX_GATEWAY_TEST_BACKEND_BINARY")
	if backendBinary == "" {
		t.Skip("set NOX_GATEWAY_TEST_BACKEND_BINARY and NOX_GATEWAY_TEST_GATEWAY_BINARY to run the actual Backend integration (required in CI)")
	}
	gatewayBinary := os.Getenv("NOX_GATEWAY_TEST_GATEWAY_BINARY")
	if gatewayBinary == "" {
		t.Fatal("NOX_GATEWAY_TEST_GATEWAY_BINARY is required with the Backend fixture")
	}
	if _, err := os.Stat(backendBinary); err != nil {
		t.Fatal("missing actual Backend binary")
	}
	if _, err := os.Stat(gatewayBinary); err != nil {
		t.Fatal("missing actual Gateway binary")
	}
	ca := testutil.CA(t)
	_, certificate, key := ca.Issue(t, "127.0.0.1", nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	certFile := testutil.File(t, "tls.crt", certificate, false)
	keyFile := testutil.File(t, "tls.key", key, true)
	caFile := testutil.File(t, "root.crt", ca.PEM, false)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.PEM)
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	backendAddr, gatewayAddr := port(t), port(t)
	backendURL, gatewayURL := "https://"+backendAddr, "https://"+gatewayAddr
	_, gatewayPort, _ := net.SplitHostPort(gatewayAddr)
	_, backendPort, _ := net.SplitHostPort(backendAddr)
	backendData := filepath.Join(t.TempDir(), "backend")
	gatewayData := filepath.Join(t.TempDir(), "gateway")
	backendEnv := append(cleanEnv(), "NOX_BACKEND_ADDR="+backendAddr, "NOX_BACKEND_DATA_DIR="+backendData, "NOX_BACKEND_PUBLIC_URL="+backendURL, "NOX_BACKEND_TLS_CERT_FILE="+certFile, "NOX_BACKEND_TLS_KEY_FILE="+keyFile, "NOX_BACKEND_SUITE_ALLOWED_CIDRS=127.0.0.1/32", "NOX_BACKEND_SUITE_ALLOWED_PORTS="+gatewayPort, "NOX_BACKEND_ADMIN_EMAILS=admin@example.invalid")
	b := start(t, backendBinary, backendEnv)
	wait(t, client, backendURL+"/v1/health", func(status int, _ []byte) bool { return status == 200 })
	b.stop(t)
	// Read only a disposable fixture key while Backend is stopped. No installed
	// data or Google account is used, and the key never enters logs or arguments.
	python := exec.Command("python3", "-c", `import sqlite3,sys
with sqlite3.connect(sys.argv[1]) as db:
 print(db.execute("SELECT key_value FROM api_keys WHERE user_id=(SELECT id FROM users WHERE email='admin@example.invalid')").fetchone()[0])`, filepath.Join(backendData, "nox-backend.db"))
	output, err := python.Output()
	if err != nil {
		t.Fatal("could not read disposable administrator fixture key")
	}
	adminKey := strings.TrimSpace(string(output))
	b = start(t, backendBinary, backendEnv)
	wait(t, client, backendURL+"/v1/health", func(status int, _ []byte) bool { return status == 200 })
	status, intent := request(t, client, "POST", backendURL+"/api/nox/pairings", adminKey, rand.Text(), "", map[string]any{"contractVersion": "1.0", "productId": "nox-gateway", "baseUrl": gatewayURL, "purpose": "gateway"})
	if status != 201 {
		t.Fatalf("actual Backend rejected initial pairing: HTTP %d", status)
	}
	intentFile := testutil.File(t, "pairing.json", intent, true)
	gatewayEnv := append(cleanEnv(), "NOX_GATEWAY_LISTEN_ADDR="+gatewayAddr, "NOX_GATEWAY_DATA_DIR="+gatewayData, "NOX_GATEWAY_PUBLIC_URL="+gatewayURL, "NOX_GATEWAY_BACKEND_URL="+backendURL, "NOX_GATEWAY_TLS_CERT_FILE="+certFile, "NOX_GATEWAY_TLS_KEY_FILE="+keyFile, "NOX_GATEWAY_TLS_CA_FILE="+caFile, "NOX_GATEWAY_PAIRING_FILE="+intentFile, "NOX_GATEWAY_SUITE_ALLOWED_CIDRS=127.0.0.1/32", "NOX_GATEWAY_SUITE_ALLOWED_PORTS="+backendPort)
	g := start(t, gatewayBinary, gatewayEnv)
	paired := func(status int, b []byte) bool {
		return status == 503 && bytes.Contains(b, []byte(`"serviceConnection":"paired"`))
	}
	wait(t, client, gatewayURL+"/readyz", paired)
	var declaration map[string]any
	status, raw := request(t, client, "GET", gatewayURL+"/nox/integration", "", "", "", nil)
	if status != 200 || json.Unmarshal(raw, &declaration) != nil || declaration["productId"] != "nox-gateway" {
		t.Fatal("actual declaration was not served")
	}
	status, raw = request(t, client, "GET", backendURL+"/api/nox/services", adminKey, "", "", nil)
	var services []map[string]any
	if status != 200 || json.Unmarshal(raw, &services) != nil || len(services) != 1 || services[0]["state"] != "active" {
		t.Fatal("actual Backend did not activate exactly one Gateway association")
	}
	firstIdentity, err := os.ReadFile(filepath.Join(gatewayData, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := services[0]["service"].(map[string]any)
	oldCredential := service["credentialId"]
	g.stop(t)
	// Offline commands use the same persistent trust; no key is put in arguments.
	rotate := exec.Command(gatewayBinary, "--service-operation=rotate")
	rotate.Env = gatewayEnv
	rotated, err := rotate.CombinedOutput()
	if err != nil || !bytes.Contains(rotated, []byte(`"serviceConnection":"paired"`)) {
		t.Fatal("actual credential rotation failed")
	}
	g = start(t, gatewayBinary, gatewayEnv)
	wait(t, client, gatewayURL+"/readyz", paired)
	after, err := os.ReadFile(filepath.Join(gatewayData, "identity.json"))
	if err != nil || !bytes.Equal(firstIdentity, after) {
		t.Fatal("actual restart changed discovery identity")
	}
	status, raw = request(t, client, "GET", backendURL+"/api/nox/services", adminKey, "", "", nil)
	if status != 200 || json.Unmarshal(raw, &services) != nil || len(services) != 1 {
		t.Fatal("actual rotation duplicated service association")
	}
	service = services[0]["service"].(map[string]any)
	if service["credentialId"] == oldCredential {
		t.Fatal("actual rotation did not change credential generation")
	}
	g.stop(t)
	revoke := exec.Command(gatewayBinary, "--service-operation=revoke-service")
	revoke.Env = gatewayEnv
	if _, err = revoke.CombinedOutput(); err != nil {
		t.Fatal("actual service revocation failed")
	}
	status, raw = request(t, client, "GET", backendURL+"/api/nox/services", adminKey, "", "", nil)
	if status != 200 || json.Unmarshal(raw, &services) != nil || len(services) != 1 || services[0]["state"] != "revoked" {
		t.Fatal("actual Backend retained an active revoked service")
	}
	g = start(t, gatewayBinary, gatewayEnv)
	wait(t, client, gatewayURL+"/readyz", func(status int, b []byte) bool {
		return status == 503 && bytes.Contains(b, []byte(`"serviceConnection":"revoked"`))
	})
	g.stop(t)
	b.stop(t)
	for _, p := range []string{filepath.Join(gatewayData, "trust.enc"), g.logs.Name()} {
		contents, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, []byte(adminKey)) {
			t.Fatal("personal fixture key leaked into Gateway data/logs")
		}
	}
}
