// Package config validates installation settings before opening state/listeners.
package config

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"encoding/base64"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

type LookupEnv func(string) (string, bool)

type Config struct {
	ListenAddr        string
	DataDir           string
	LogLevel          slog.Level
	ShutdownTimeout   time.Duration
	PublicURL         string
	BackendURL        string
	TLSCertFile       string
	TLSKeyFile        string
	PublicTLSCertFile string
	TLSCAFile         string
	BackendTLSPin     string
	PairingFile       string
	AllowedCIDRs      []netip.Prefix
	AllowedPorts      []uint16
	TrustedProxyCIDRs []netip.Prefix
}

// Load returns static errors that name the setting, never its supplied value.
func Load(lookup LookupEnv) (Config, error) {
	var c Config
	value := func(name, fallback string) string {
		if v, ok := lookup(name); ok {
			return v
		}
		return fallback
	}
	c.ListenAddr = value("NOX_GATEWAY_LISTEN_ADDR", "127.0.0.1:8080")
	host, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil || strings.TrimSpace(c.ListenAddr) != c.ListenAddr {
		return c, errors.New("NOX_GATEWAY_LISTEN_ADDR must be an IP:port or localhost:port")
	}
	if host != "" && host != "localhost" {
		ip, e := netip.ParseAddr(host)
		if e != nil || ip.Zone() != "" {
			return c, errors.New("NOX_GATEWAY_LISTEN_ADDR must use a local bind IP or localhost")
		}
	}
	if port == "" || strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return c, errors.New("NOX_GATEWAY_LISTEN_ADDR must have a numeric port")
	}
	_, err = strconv.ParseUint(port, 10, 16)
	if err != nil || (len(port) > 1 && port[0] == '0') {
		return c, errors.New("NOX_GATEWAY_LISTEN_ADDR has an invalid port")
	}
	// Port zero deliberately permits an OS-assigned local diagnostic port.
	c.DataDir = value("NOX_GATEWAY_DATA_DIR", "./data")
	if strings.TrimSpace(c.DataDir) == "" || strings.ContainsAny(c.DataDir, "\x00\r\n") {
		return c, errors.New("NOX_GATEWAY_DATA_DIR must be a non-empty filesystem path")
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return c, errors.New("NOX_GATEWAY_DATA_DIR cannot be resolved")
	}
	switch value("NOX_GATEWAY_LOG_LEVEL", "info") {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		return c, errors.New("NOX_GATEWAY_LOG_LEVEL must be debug, info, warn, or error")
	}
	ms, err := strconv.ParseUint(value("NOX_GATEWAY_SHUTDOWN_TIMEOUT_MS", "10000"), 10, 32)
	if err != nil || ms < 1 || ms > 60000 {
		return c, errors.New("NOX_GATEWAY_SHUTDOWN_TIMEOUT_MS must be between 1 and 60000")
	}
	c.ShutdownTimeout = time.Duration(ms) * time.Millisecond
	c.PublicURL = value("NOX_GATEWAY_PUBLIC_URL", "")
	c.BackendURL = value("NOX_GATEWAY_BACKEND_URL", "")
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"NOX_GATEWAY_PUBLIC_URL", &c.PublicURL}, {"NOX_GATEWAY_BACKEND_URL", &c.BackendURL},
	} {
		if *item.target != "" {
			normalized, err := noxmcp.NormalizeSuiteOrigin(*item.target)
			if err != nil {
				return c, errors.New(item.name + " must be an HTTPS origin without an application prefix")
			}
			*item.target = normalized
		}
	}
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"NOX_GATEWAY_TLS_CERT_FILE", &c.TLSCertFile}, {"NOX_GATEWAY_TLS_KEY_FILE", &c.TLSKeyFile},
		{"NOX_GATEWAY_PUBLIC_TLS_CERT_FILE", &c.PublicTLSCertFile}, {"NOX_GATEWAY_TLS_CA_FILE", &c.TLSCAFile},
		{"NOX_GATEWAY_PAIRING_FILE", &c.PairingFile},
	} {
		*item.target = value(item.name, "")
		if strings.ContainsAny(*item.target, "\x00\r\n") {
			return c, errors.New(item.name + " must be a filesystem path")
		}
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return c, errors.New("configure NOX_GATEWAY_TLS_CERT_FILE and NOX_GATEWAY_TLS_KEY_FILE together")
	}
	c.BackendTLSPin = value("NOX_GATEWAY_BACKEND_TLS_PIN", "")
	if c.BackendTLSPin != "" {
		b, err := base64.RawURLEncoding.DecodeString(c.BackendTLSPin)
		if err != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != c.BackendTLSPin {
			return c, errors.New("NOX_GATEWAY_BACKEND_TLS_PIN must be a base64url SHA-256 SPKI fingerprint")
		}
	}
	c.AllowedCIDRs, err = parseCIDRs(value("NOX_GATEWAY_SUITE_ALLOWED_CIDRS", ""))
	if err != nil {
		return c, errors.New("NOX_GATEWAY_SUITE_ALLOWED_CIDRS must contain canonical IP networks")
	}
	c.TrustedProxyCIDRs, err = parseCIDRs(value("NOX_GATEWAY_TRUSTED_PROXY_CIDRS", ""))
	if err != nil {
		return c, errors.New("NOX_GATEWAY_TRUSTED_PROXY_CIDRS must contain canonical proxy IP networks")
	}
	for _, port := range strings.Split(value("NOX_GATEWAY_SUITE_ALLOWED_PORTS", "443"), ",") {
		port = strings.TrimSpace(port)
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 || (len(port) > 1 && port[0] == '0') || strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return c, errors.New("NOX_GATEWAY_SUITE_ALLOWED_PORTS must contain ports from 1 to 65535")
		}
		c.AllowedPorts = append(c.AllowedPorts, uint16(n))
	}
	if c.PublicURL == "" && (c.TLSCertFile != "" || c.PublicTLSCertFile != "" || len(c.TrustedProxyCIDRs) > 0 || c.BackendURL != "") {
		return c, errors.New("configure NOX_GATEWAY_PUBLIC_URL before enabling Suite TLS or Backend communication")
	}
	if c.PublicURL != "" && c.TLSCertFile == "" && (c.PublicTLSCertFile == "" || len(c.TrustedProxyCIDRs) == 0) {
		return c, errors.New("configure direct TLS or an explicit trusted proxy and its public certificate")
	}
	if c.BackendURL != "" && len(c.AllowedCIDRs) == 0 {
		return c, errors.New("NOX_GATEWAY_SUITE_ALLOWED_CIDRS must explicitly approve Backend targets")
	}
	if c.BackendURL == "" && (c.PairingFile != "" || c.BackendTLSPin != "") {
		return c, errors.New("configure NOX_GATEWAY_BACKEND_URL before supplying pairing or Backend trust")
	}
	return c, nil
}

func parseCIDRs(value string) ([]netip.Prefix, error) {
	var networks []netip.Prefix
	if value == "" {
		return networks, nil
	}
	for _, entry := range strings.Split(value, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(entry))
		if err != nil || p != p.Masked() || p.Addr().Is4In6() {
			return nil, errors.New("invalid network")
		}
		networks = append(networks, p)
	}
	return networks, nil
}
