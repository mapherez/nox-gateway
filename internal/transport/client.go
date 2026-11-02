// Package transport carries Suite service traffic exclusively over same-host sockets.
package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mapherez/nox-gateway/internal/config"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

var ErrUnavailable = errors.New("local Suite participant is unavailable")
var ErrTarget = errors.New("target is not a canonical same-host Suite participant")
var ErrRedirect = errors.New("Suite redirects are not followed")

type Client struct{ HTTP *http.Client }

func Origin(id string) string {
	host := id
	numeric := id != ""
	for _, r := range id {
		if r < '0' || r > '9' {
			numeric = false
			break
		}
	}
	if len(id) > 63 || numeric {
		if len(id) > 32 {
			host = id[:32] + "." + id[32:]
		}
		host += ".nox-suite"
	}
	return "http://" + host
}
func Directory(dir string) error {
	if !filepath.IsAbs(dir) {
		return ErrTarget
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&7 != 0 {
		return ErrTarget
	}
	return nil
}
func Socket(dir, origin string) (string, error) {
	normalized, err := noxmcp.NormalizeSuiteOrigin(origin)
	if err != nil || normalized != origin {
		return "", ErrTarget
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Port() != "" {
		return "", ErrTarget
	}
	id := u.Host
	if strings.HasSuffix(id, ".nox-suite") {
		id = strings.ReplaceAll(strings.TrimSuffix(id, ".nox-suite"), ".", "")
	}
	if !noxmcp.ValidSuiteProductID(id) || Origin(id) != origin {
		return "", ErrTarget
	}
	if err = Directory(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".sock"), nil
}
func NewLocal(dir, origin string) (*Client, error) {
	path, err := Socket(dir, origin)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxConnsPerHost: 32, MaxResponseHeaderBytes: 32 << 10}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != strings.TrimPrefix(origin, "http://")+":80" {
			return nil, ErrTarget
		}
		if err := Directory(dir); err != nil {
			return nil, err
		}
		st, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrUnavailable
		}
		if err != nil || st.Mode()&os.ModeSocket == 0 {
			return nil, ErrTarget
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
	}
	return &Client{HTTP: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect }}}, nil
}
func NewClient(c config.Config) (*Client, error) { return NewLocal(c.SocketDir, "http://nox-backend") }
func (c *Client) Close()                         { c.HTTP.CloseIdleConnections() }
