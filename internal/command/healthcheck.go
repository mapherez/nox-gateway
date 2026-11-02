package command

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/transport"
)

func healthcheck(ctx context.Context, c config.Config) error {
	host, port, e := net.SplitHostPort(c.ListenAddr)
	if e != nil {
		return errors.New("invalid healthcheck listener")
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	scheme := "http"
	tr := &http.Transport{Proxy: nil}
	defer tr.CloseIdleConnections()
	if c.TLSCertFile != "" {
		cert, e := transport.PublicCertificate(c)
		if e != nil {
			return e
		}
		roots := x509.NewCertPool()
		roots.AddCert(cert)
		u, _ := url.Parse(c.PublicURL)
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: u.Hostname()}
		scheme = "https"
	}
	req, e := http.NewRequestWithContext(ctx, "GET", scheme+"://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if e != nil {
		return e
	}
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		return errors.New("Gateway healthcheck failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("Gateway healthcheck failed")
	}
	return nil
}
