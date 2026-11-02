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

	noxmcp "github.com/mapherez/nox-mcp/go"
)

type LookupEnv func(string) (string, bool)

type Config struct {
	ListenAddr      string
	DataDir         string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	PublicURL       string
	BackendURL      string
	TLSCertFile     string
	TLSKeyFile      string
	SocketDir       string
	CallTimeout     time.Duration
	ProbeInterval   time.Duration
	MaxConcurrent   int
	MaxPayloadBytes int
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

	limits := []struct {
		name, fallback string
		min, max       uint64
		apply          func(uint64)
	}{
		{"NOX_GATEWAY_CALL_TIMEOUT_MS", "20000", 10000, 3600000, func(v uint64) { c.CallTimeout = time.Duration(v) * time.Millisecond }},
		{"NOX_GATEWAY_PROBE_INTERVAL_MS", "15000", 1000, 3600000, func(v uint64) { c.ProbeInterval = time.Duration(v) * time.Millisecond }},
		{"NOX_GATEWAY_MAX_CONCURRENT", "32", 1, 256, func(v uint64) { c.MaxConcurrent = int(v) }},
		{"NOX_GATEWAY_MAX_PAYLOAD_BYTES", "2097152", 1024, noxmcp.SuiteMaxPayloadBytes, func(v uint64) { c.MaxPayloadBytes = int(v) }},
	}
	for _, limit := range limits {
		n, err := strconv.ParseUint(value(limit.name, limit.fallback), 10, 64)
		if err != nil || n < limit.min || n > limit.max {
			return c, errors.New(limit.name + " is outside its supported range")
		}
		limit.apply(n)
	}
	c.PublicURL = value("NOX_GATEWAY_PUBLIC_URL", "")
	if c.PublicURL != "" {
		c.PublicURL, err = noxmcp.NormalizeSuiteOrigin(c.PublicURL)
		if err != nil {
			return c, errors.New("NOX_GATEWAY_PUBLIC_URL must be an HTTP or HTTPS origin without an application prefix")
		}
	}
	c.SocketDir = value("NOX_GATEWAY_SUITE_SOCKET_DIR", "")
	if c.SocketDir != "" && (!filepath.IsAbs(c.SocketDir) || strings.ContainsAny(c.SocketDir, "\x00\r\n")) {
		return c, errors.New("NOX_GATEWAY_SUITE_SOCKET_DIR must be an absolute local directory")
	}
	// Suite identities are logical origins, never DNS/TCP targets or native URLs.
	if c.SocketDir != "" {
		c.BackendURL = "http://nox-backend"
	}
	c.TLSCertFile = value("NOX_GATEWAY_TLS_CERT_FILE", "")
	c.TLSKeyFile = value("NOX_GATEWAY_TLS_KEY_FILE", "")
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return c, errors.New("configure NOX_GATEWAY_TLS_CERT_FILE and NOX_GATEWAY_TLS_KEY_FILE together")
	}
	if c.TLSCertFile != "" && !strings.HasPrefix(c.PublicURL, "https://") {
		return c, errors.New("direct native TLS requires an HTTPS NOX_GATEWAY_PUBLIC_URL")
	}
	return c, nil
}
