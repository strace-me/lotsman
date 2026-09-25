package dataplane

import (
	"context"
	"net"
	"strings"

	utls "github.com/metacubex/utls"
)

func uTLSDialContext(dial func(context.Context, string, string) (net.Conn, error), fingerprint string) func(context.Context, string, string) (net.Conn, error) {
	helloID, ok := uTLSHelloID(fingerprint)
	if !ok {
		return nil
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tlsCtx, cancel := context.WithTimeout(ctx, TLSPhaseTimeout)
		defer cancel()
		cfg := &utls.Config{
			ServerName: host,
			NextProtos: []string{"h2", "http/1.1"},
		}
		uconn := utls.UClient(conn, cfg, helloID)
		if err := uconn.HandshakeContext(tlsCtx); err != nil {
			conn.Close()
			return nil, err
		}
		return uconn, nil
	}
}

func uTLSHelloID(fp string) (utls.ClientHelloID, bool) {
	switch strings.ToLower(strings.TrimSpace(fp)) {
	case "chrome", "":
		return utls.HelloChrome_Auto, true
	case "firefox":
		return utls.HelloFirefox_Auto, true
	case "safari":
		return utls.HelloSafari_Auto, true
	case "edge":
		return utls.HelloEdge_Auto, true
	case "ios":
		return utls.HelloIOS_Auto, true
	default:
		return utls.HelloChrome_Auto, true
	}
}
