package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/testutil"
)

func tlsServer(t *testing.T, pair tls.Certificate, handler http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}
func targetConfig(t *testing.T, origin string) config.Config {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return config.Config{BackendURL: origin, AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, AllowedPorts: []uint16{uint16(p)}}
}

func TestPrivateCAAndExplicitPinsValidateBeforeSendingCredentials(t *testing.T) {
	ca := testutil.CA(t)
	pair, _, _ := ca.Issue(t, "127.0.0.1", nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	var received atomic.Int32
	s := tlsServer(t, pair, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
	for _, mode := range []string{"untrusted", "ca", "pin", "wrong-pin"} {
		t.Run(mode, func(t *testing.T) {
			c := targetConfig(t, s.URL)
			if mode == "ca" {
				c.TLSCAFile = testutil.File(t, "root.crt", ca.PEM, false)
			}
			if mode == "pin" {
				c.BackendTLSPin = SPKI(pair.Leaf)
			}
			if mode == "wrong-pin" {
				c.BackendTLSPin = strings.Repeat("A", 43)
				c.TLSCAFile = testutil.File(t, "root.crt", ca.PEM, false)
			}
			out, err := NewClient(c)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			before := received.Load()
			r, _ := http.NewRequestWithContext(t.Context(), "GET", s.URL, nil)
			r.Header.Set("Authorization", "Bearer PRIVATE")
			response, err := out.HTTP.Do(r)
			if response != nil {
				response.Body.Close()
			}
			success := mode == "ca" || mode == "pin"
			if (err == nil) != success {
				t.Fatalf("unexpected TLS result for %s: %v", mode, err)
			}
			if !success && received.Load() != before {
				t.Fatal("credential reached an untrusted peer")
			}
		})
	}
	expired, _, _ := ca.Issue(t, "127.0.0.1", nil, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	expiredServer := tlsServer(t, expired, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("expired pinned certificate accepted a request") }))
	c := targetConfig(t, expiredServer.URL)
	c.BackendTLSPin = SPKI(expired.Leaf)
	out, err := NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if response, err := out.HTTP.Get(expiredServer.URL); err == nil {
		response.Body.Close()
		t.Fatal("pin bypassed certificate expiry")
	}
}

func TestTargetPolicyChecksAllDNSResultsAndRechecksReconnects(t *testing.T) {
	c := config.Config{BackendURL: "https://backend.home.arpa", AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("192.168.10.0/24")}, AllowedPorts: []uint16{443}}
	out, err := NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var dials int
	out.dial = func(context.Context, string, string) (net.Conn, error) {
		dials++
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	ips := []netip.Addr{netip.MustParseAddr("192.168.10.10"), netip.MustParseAddr("1.1.1.1")}
	out.resolve = func(context.Context, string, string) ([]netip.Addr, error) { return ips, nil }
	if _, err = out.dialTarget(t.Context(), "tcp", "backend.home.arpa:443"); err == nil || dials != 0 {
		t.Fatal("dialed before rejecting a mixed DNS answer")
	}
	ips = ips[:1]
	conn, err := out.dialTarget(t.Context(), "tcp", "backend.home.arpa:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	ips = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
	if _, err = out.dialTarget(t.Context(), "tcp", "backend.home.arpa:443"); err == nil || dials != 1 {
		t.Fatal("reconnect reused stale DNS approval")
	}
	for _, addr := range []string{"other.home.arpa:443", "backend.home.arpa:8443"} {
		if _, err = out.dialTarget(t.Context(), "tcp", addr); err == nil {
			t.Fatal("changed target accepted")
		}
	}
}

func TestRedirectsAndEnvironmentProxiesNeverReceiveCredentials(t *testing.T) {
	var forwarded atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer destination.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer s.Close()
	t.Setenv("HTTPS_PROXY", destination.URL)
	t.Setenv("HTTP_PROXY", destination.URL)
	t.Setenv("ALL_PROXY", destination.URL)
	c := targetConfig(t, s.URL)
	c.BackendTLSPin = SPKI(s.Certificate())
	out, err := NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	request, _ := http.NewRequestWithContext(t.Context(), "GET", s.URL, nil)
	request.Header.Set("Authorization", "Bearer PRIVATE")
	response, err := out.HTTP.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || forwarded.Load() != 0 {
		t.Fatal("followed redirect or used environment proxy")
	}
}

func TestServerReloadsRenewedCertificateButRejectsUnexpectedKeyChange(t *testing.T) {
	ca := testutil.CA(t)
	first, cert, key := ca.Issue(t, "gateway.home.arpa", nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	c := config.Config{PublicURL: "https://gateway.home.arpa", TLSCertFile: testutil.File(t, "tls.crt", cert, false), TLSKeyFile: testutil.File(t, "tls.key", key, true)}
	cfg, err := ServerTLS(c)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := cfg.GetCertificate(nil)
	if err != nil || loaded.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 {
		t.Fatal("initial certificate not loaded")
	}
	renewed, cert, key := ca.Issue(t, "gateway.home.arpa", first.PrivateKey.(*ecdsa.PrivateKey), time.Now().Add(-time.Hour), time.Now().Add(2*time.Hour))
	if err = os.WriteFile(c.TLSCertFile, cert, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(c.TLSKeyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = cfg.GetCertificate(nil)
	if err != nil || loaded.Leaf.SerialNumber.Cmp(renewed.Leaf.SerialNumber) != 0 {
		t.Fatal("same-key renewal was not reloaded")
	}
	_, cert, key = ca.Issue(t, "gateway.home.arpa", nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err = os.WriteFile(c.TLSCertFile, cert, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(c.TLSKeyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = cfg.GetCertificate(nil); err == nil {
		t.Fatal("unapproved public key change was accepted")
	}
	if err = os.Chmod(c.TLSKeyFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = ServerTLS(c); err == nil {
		t.Fatal("publicly readable private key was accepted")
	}
	wrong := c
	wrong.PublicURL = "https://different.home.arpa"
	if _, err = PublicCertificate(wrong); err == nil {
		t.Fatal("wrong certificate hostname accepted")
	}
}

func TestHTTPSRequiresTheConfiguredProxyPeerAndUnambiguousScheme(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("192.168.10.10/32")}
	for _, tc := range []struct {
		peer, scheme         string
		tls, duplicate, want bool
	}{
		{"192.168.10.10:5000", "https", false, false, true}, {"192.168.10.11:5000", "https", false, false, false},
		{"192.168.10.10:5000", "http", false, false, false}, {"192.168.10.10:5000", "https,http", false, false, false},
		{"192.168.10.10:5000", "https", false, true, false}, {"192.168.10.11:5000", "", true, false, true},
	} {
		r := httptest.NewRequest("POST", "http://gateway.home.arpa/mcp", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-Proto", tc.scheme)
		if tc.duplicate {
			r.Header.Add("X-Forwarded-Proto", "https")
		}
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if IsHTTPS(r, trusted) != tc.want {
			t.Fatalf("wrong proxy boundary result: %#v", tc)
		}
	}
}
