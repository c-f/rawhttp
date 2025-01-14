package rawhttp

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/projectdiscovery/rawhttp/client"
	"github.com/projectdiscovery/rawhttp/proxy"
)

// Dialer can dial a remote HTTP server.
type Dialer interface {
	// Dial dials a remote http server returning a Conn.
	Dial(protocol, addr string) (Conn, error)
	DialWithProxy(protocol, addr, proxyURL string, timeout time.Duration) (Conn, error)
	// Dial dials a remote http server with timeout returning a Conn.
	DialTimeout(protocol, addr string, timeout time.Duration) (Conn, error)
}

type dialer struct {
	sync.Mutex                   // protects following fields
	conns      map[string][]Conn // maps addr to a, possibly empty, slice of existing Conns
}

func (d *dialer) Dial(protocol, addr string) (Conn, error) {
	return d.dialTimeout(protocol, addr, 0)
}

func (d *dialer) DialTimeout(protocol, addr string, timeout time.Duration) (Conn, error) {
	return d.dialTimeout(protocol, addr, timeout)
}

func (d *dialer) dialTimeout(protocol, addr string, timeout time.Duration) (Conn, error) {
	d.Lock()
	if d.conns == nil {
		d.conns = make(map[string][]Conn)
	}
	if c, ok := d.conns[addr]; ok {
		if len(c) > 0 {
			conn := c[0]
			c[0] = c[len(c)-1]
			d.Unlock()
			return conn, nil
		}
	}
	d.Unlock()
	c, err := clientDial(protocol, addr, timeout)
	return &conn{
		Client: client.NewClient(c),
		Conn:   c,
		dialer: d,
	}, err
}

func (d *dialer) DialWithProxy(protocol, addr, proxyURL string, timeout time.Duration) (Conn, error) {
	var c net.Conn
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("unsupported proxy error: %w", err)
	}
	switch u.Scheme {
	case "http":
		c, err = proxy.HTTPDialer(proxyURL, timeout)(addr)
	case "socks5", "socks5h":
		c, err = proxy.Socks5Dialer(proxyURL, timeout)(addr)
	default:
		return nil, fmt.Errorf("unsupported proxy protocol: %s", proxyURL)
	}
	if err != nil {
		return nil, fmt.Errorf("proxy error: %w", err)
	}
	if protocol == "https" {
		if c, err = TlsHandshake(c, addr); err != nil {
			return nil, fmt.Errorf("tls handshake error: %w", err)
		}
	}
	return &conn{
		Client: client.NewClient(c),
		Conn:   c,
		dialer: d,
	}, err
}

func clientDial(protocol, addr string, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if protocol == "https" {
		if conn, err = TlsHandshake(conn, addr); err != nil {
			return nil, fmt.Errorf("tls handshake error: %w", err)
		}
	}
	return conn, err
}

// TlsHandshake tls handshake on a plain connection
func TlsHandshake(conn net.Conn, addr string) (net.Conn, error) {
	colonPos := strings.LastIndex(addr, ":")
	if colonPos == -1 {
		colonPos = len(addr)
	}
	hostname := addr[:colonPos]

	tlsConn := tls.Client(conn, &tls.Config{
		InsecureSkipVerify: true,
		ServerName: hostname,
	})
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

// Conn is an interface implemented by a connection
type Conn interface {
	client.Client
	io.Closer

	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	Release()
}

type conn struct {
	client.Client
	net.Conn
	*dialer
}

func (c *conn) Release() {
	c.dialer.Lock()
	defer c.dialer.Unlock()
	addr := c.Conn.RemoteAddr().String()
	c.dialer.conns[addr] = append(c.dialer.conns[addr], c)
}
