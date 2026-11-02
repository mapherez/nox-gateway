// Package transport enforces the configured TLS trust boundary and target policy.
package transport

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/securefile"
)

var ErrTLS = errors.New("TLS certificate, private key, hostname, or validity does not match the configured trust")

func SPKI(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func PublicCertificate(c config.Config) (*x509.Certificate, error) {
	file := c.PublicTLSCertFile
	if file == "" {
		file = c.TLSCertFile
	}
	b, err := securefile.Read(file, false, 256<<10)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, ErrTLS
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, ErrTLS
	}
	origin, err := url.Parse(c.PublicURL)
	if err != nil || cert.IsCA || cert.VerifyHostname(origin.Hostname()) != nil || !validNow(cert) {
		return nil, ErrTLS
	}
	return cert, nil
}

func validNow(c *x509.Certificate) bool {
	now := time.Now()
	return !now.Before(c.NotBefore) && now.Before(c.NotAfter)
}

// ServerTLS reloads same-key certificates on handshake. An unexpected key change
// fails closed until explicit restart/re-pairing; renewal does not reset trust.
func ServerTLS(c config.Config) (*tls.Config, error) {
	if c.TLSCertFile == "" {
		return nil, nil
	}
	load := func() (*tls.Certificate, error) {
		cert, err := securefile.Read(c.TLSCertFile, false, 256<<10)
		if err != nil {
			return nil, err
		}
		key, err := securefile.Read(c.TLSKeyFile, true, 64<<10)
		if err != nil {
			return nil, err
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, ErrTLS
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || leaf.IsCA || !validNow(leaf) {
			return nil, ErrTLS
		}
		if c.PublicTLSCertFile == "" {
			u, _ := url.Parse(c.PublicURL)
			if u == nil || leaf.VerifyHostname(u.Hostname()) != nil {
				return nil, ErrTLS
			}
		}
		pair.Leaf = leaf
		return &pair, nil
	}
	initial, err := load()
	if err != nil {
		return nil, err
	}
	pin := SPKI(initial.Leaf)
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		pair, err := load()
		if err != nil {
			return nil, err
		}
		if SPKI(pair.Leaf) != pin {
			return nil, ErrTLS
		}
		return pair, nil
	}}, nil
}

// IsHTTPS never trusts a forwarding claim without the immediate peer binding.
func IsHTTPS(r *http.Request, trusted []netip.Prefix) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	allowed := false
	for _, p := range trusted {
		if p.Contains(ip.Unmap()) {
			allowed = true
			break
		}
	}
	if !allowed || len(r.Header.Values("X-Forwarded-Proto")) != 1 {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
