package transport

import (
	"context"
	"crypto/ecdsa"
	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/testutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestOnlyCanonicalLocalSocketsCanReceiveServiceCredentials(t *testing.T) {
	dir, e := os.MkdirTemp("", "ng-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	listener, cleanup, e := Listen(dir, "nox-backend")
	if e != nil {
		t.Fatal(e)
	}
	defer cleanup()
	seen := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})}
	defer srv.Close()
	go srv.Serve(listener)
	client, e := NewLocal(dir, "http://nox-backend")
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://nox-backend/api/nox/keys", nil)
	req.Header.Set("Authorization", "Bearer credential")
	res, e := client.HTTP.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if <-seen != "Bearer credential" {
		t.Fatal("credential not delivered locally")
	}
	for _, origin := range []string{"https://nox-backend", "http://127.0.0.1", "http://192.0.2.1", "http://nox-backend:8080", "http://nox-backend/path"} {
		if _, e := NewLocal(dir, origin); e == nil {
			t.Fatal("network destination accepted", origin)
		}
	}
	if _, _, e := Listen(dir, "nox-backend"); e == nil {
		t.Fatal("active socket was replaced")
	}
	if e := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "other.sock")); e != nil {
		t.Fatal(e)
	}
	other, e := NewLocal(dir, "http://other")
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	r, _ := http.NewRequestWithContext(context.Background(), "GET", "http://other/", nil)
	if _, e = other.HTTP.Do(r); e == nil {
		t.Fatal("symlink accepted")
	}
	for _, id := range []string{"1", strings.Repeat("x", 64), strings.Repeat("2", 64)} {
		p, e := Socket(dir, Origin(id))
		if e != nil || filepath.Base(p) != id+".sock" {
			t.Fatal("canonical product mapping failed", e)
		}
	}
}
