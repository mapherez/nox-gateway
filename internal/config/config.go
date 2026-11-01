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
)

type LookupEnv func(string) (string, bool)

type Config struct {
	ListenAddr      string
	DataDir         string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
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
	return c, nil
}
