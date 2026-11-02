package transport

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/securefile"
)

var (
	ErrTarget   = errors.New("target is outside the approved origin, ports, or IP networks")
	ErrRedirect = errors.New("credential-bearing requests cannot follow redirects")
)

type Client struct {
	HTTP     *http.Client
	origin   *url.URL
	networks []netip.Prefix
	ports    []uint16
	resolve  func(context.Context, string, string) ([]netip.Addr, error)
	dial     func(context.Context, string, string) (net.Conn, error)
}

func NewClient(c config.Config) (*Client, error) {
	origin, err := url.Parse(c.BackendURL)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || len(c.AllowedCIDRs) == 0 {
		return nil, ErrTarget
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, ErrTLS
	}
	if c.TLSCAFile != "" {
		b, err := securefile.Read(c.TLSCAFile, false, 256<<10)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, ErrTLS
		}
	}
	out := &Client{origin: origin, networks: append([]netip.Prefix(nil), c.AllowedCIDRs...), ports: append([]uint16(nil), c.AllowedPorts...), resolve: net.DefaultResolver.LookupNetIP, dial: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	if c.BackendTLSPin != "" {
		// Explicit administrator-provisioned SPKI mode replaces PKI validation, never
		// a fallback. The peer key and certificate lifetime are always verified.
		tlsConfig.InsecureSkipVerify = true
		tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !validNow(cs.PeerCertificates[0]) || subtle.ConstantTimeCompare([]byte(SPKI(cs.PeerCertificates[0])), []byte(c.BackendTLSPin)) != 1 {
				return ErrTLS
			}
			return nil
		}
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: out.dialTarget, TLSClientConfig: tlsConfig,
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second,
		IdleConnTimeout: 60 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 8,
		MaxConnsPerHost: 8, MaxResponseHeaderBytes: 32 << 10,
	}
	out.HTTP = &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect }}
	return out, nil
}

func (c *Client) Close() { c.HTTP.CloseIdleConnections() }

func (c *Client) dialTarget(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	expectedPort := c.origin.Port()
	if expectedPort == "" {
		expectedPort = "443"
	}
	if err != nil || host != c.origin.Hostname() || port != expectedPort {
		return nil, ErrTarget
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, ErrTarget
	}
	approved := false
	for _, p := range c.ports {
		if uint16(n) == p {
			approved = true
			break
		}
	}
	if !approved {
		return nil, ErrTarget
	}
	lookup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := c.resolve(lookup, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, ErrTarget
	}
	// Check every result before attempting any connection; DNS changes are
	// re-evaluated for each new connection, then dial the actual approved IP.
	for _, ip := range ips {
		allowed := false
		for _, p := range c.networks {
			if p.Contains(ip.Unmap()) {
				allowed = true
				break
			}
		}
		if !allowed || ip.Zone() != "" {
			return nil, ErrTarget
		}
	}
	for _, ip := range ips {
		conn, e := c.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("approved target is unavailable")
}
