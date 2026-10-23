package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

const authority = "graph.facebook.com:443"
const headerLimit = 8192

var refused = errors.New("carrier proxy refused or stopped")

type settings struct {
	maxTunnels     int
	lifetime, idle time.Duration
	resolve        func(context.Context, string) ([]netip.Addr, error)
	dial           func(context.Context, string, string) (net.Conn, error)
}
type proxy struct {
	settings
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closing bool
	slots   chan struct{}
	pending sync.WaitGroup
}

// Bound accepted sockets too, including clients that never finish a header.
// Excess sockets are closed without allocating another HTTP parser goroutine.
type boundedListener struct {
	net.Listener
	slots chan struct{}
}
type boundedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *boundedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &boundedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

func newProxy(s settings) (*proxy, error) {
	if s.maxTunnels < 1 || s.maxTunnels > 64 || s.lifetime <= 0 || s.lifetime > 10*time.Minute || s.idle <= 0 || s.idle > s.lifetime {
		return nil, refused
	}
	if s.resolve == nil {
		s.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if s.dial == nil {
		s.dial = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &proxy{settings: s, ctx: ctx, cancel: cancel, slots: make(chan struct{}, s.maxTunnels)}, nil
}

// Close refuses new tunnels, cancels DNS/dial and closes hijacked connections.
// WaitGroup registration is serialized with closing, so shutdown cannot miss an
// accepted handler even though net/http cannot drain hijacked connections itself.
func (p *proxy) Close() { p.mu.Lock(); p.closing = true; p.cancel(); p.mu.Unlock(); p.pending.Wait() }
func (p *proxy) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return false
	}
	select {
	case p.slots <- struct{}{}:
		p.pending.Add(1)
		return true
	default:
		return false
	}
}

var denied = func() []netip.Prefix {
	values := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"168.63.129.16/32", // Azure host platform endpoint, never a Meta destination.
		"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"}
	result := make([]netip.Prefix, 0, len(values))
	for _, v := range values {
		result = append(result, netip.MustParsePrefix(v))
	}
	return result
}()

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range denied {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func validRequest(r *http.Request) bool {
	// Go derives Request.Host from the CONNECT request target, ignoring the wire
	// Host header. The target and parsed URL are independently exact-pinned; no
	// HTTP headers are ever sent upstream, so wire Host cannot select a dial.
	if r.Method != "CONNECT" || r.ProtoMajor != 1 || r.URL == nil || r.RequestURI != authority || r.Host != authority || r.URL.Host != authority ||
		r.URL.Scheme != "" || r.URL.User != nil || r.URL.Opaque != "" || r.URL.Path != "" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" ||
		r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 {
		return false
	}
	for _, key := range []string{"Content-Length", "Transfer-Encoding", "Trailer", "Expect", "Upgrade"} {
		if _, ok := r.Header[key]; ok {
			return false
		}
	}
	size := len(r.Method) + len(r.RequestURI) + len(r.Proto) + len(r.Host) + 16
	if len(r.Header) > 64 {
		return false
	}
	for k, values := range r.Header {
		for _, v := range values {
			size += len(k) + len(v) + 4
		}
	}
	return size <= headerLimit
}
func reject(w http.ResponseWriter, code int) {
	w.Header().Set("Connection", "close")
	http.Error(w, "connect refused", code)
}
func response(c net.Conn, status string) error {
	if c.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
		return refused
	}
	_, err := io.WriteString(c, "HTTP/1.1 "+status+"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
	return err
}

type activityReader struct {
	net.Conn
	touch func()
}

func (r activityReader) Read(b []byte) (int, error) {
	n, err := r.Conn.Read(b)
	if n > 0 {
		r.touch()
	}
	return n, err
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "CONNECT" {
		reject(w, http.StatusMethodNotAllowed)
		return
	}
	if !validRequest(r) {
		reject(w, http.StatusBadRequest)
		return
	}
	if !p.begin() {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	defer func() { <-p.slots; p.pending.Done() }()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		reject(w, http.StatusInternalServerError)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	// CONNECT has no request body. Do not interpret or forward a second buffered
	// HTTP message/body as tunnel bytes before the connection is established.
	if buffer.Reader.Buffered() != 0 {
		_ = response(client, "400 Bad Request")
		return
	}
	_ = client.SetDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(p.ctx, p.lifetime)
	defer cancel()
	stopRequest := context.AfterFunc(r.Context(), cancel)
	defer stopRequest()
	stopClient := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopClient()
	resolveCtx, resolveCancel := context.WithTimeout(ctx, 5*time.Second)
	addresses, err := p.resolve(resolveCtx, "graph.facebook.com")
	resolveCancel()
	if err != nil || len(addresses) == 0 || len(addresses) > 64 {
		_ = response(client, "502 Bad Gateway")
		return
	}
	for _, ip := range addresses {
		if !publicIP(ip) {
			_ = response(client, "403 Forbidden")
			return
		}
	}
	// One resolution, one checked literal IP, one dial. No name redial/fallback,
	// proxy environment, request-selected destination or redirect handling.
	address := net.JoinHostPort(addresses[0].Unmap().String(), "443")
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	upstream, err := p.dial(dialCtx, "tcp", address)
	dialCancel()
	if err != nil {
		_ = response(client, "502 Bad Gateway")
		return
	}
	defer upstream.Close()
	stopUpstream := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stopUpstream()
	if ctx.Err() != nil {
		return
	}
	if client.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
		return
	}
	if _, err = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	touch := func() {
		deadline := time.Now().Add(p.idle)
		_ = client.SetDeadline(deadline)
		_ = upstream.SetDeadline(deadline)
	}
	touch()
	finished := make(chan struct{}, 2)
	for _, pair := range [][2]net.Conn{{upstream, client}, {client, upstream}} {
		go func(destination, source net.Conn) {
			_, _ = io.Copy(destination, activityReader{source, touch})
			finished <- struct{}{}
		}(pair[0], pair[1])
	}
	<-finished
	_ = client.Close()
	_ = upstream.Close()
	<-finished
}

func newServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second,
		MaxHeaderBytes: headerLimit, ErrorLog: log.New(io.Discard, "", 0)}
}
func listenAddress(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return refused
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return refused
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return refused
	}
	return nil
}
