//go:build !unix

package transport

import "net"

func Listen(string, string) (net.Listener, func(), error) { return nil, nil, ErrTarget }
