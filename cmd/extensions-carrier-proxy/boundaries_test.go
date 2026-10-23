package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const connectRequest = "CONNECT graph.facebook.com:443 HTTP/1.1\r\nHost: graph.facebook.com:443\r\n\r\n"

func request(t *testing.T, url, raw string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.WriteString(conn, raw); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return conn, reader, response
}
func serve(t *testing.T, s settings) (*proxy, *httptest.Server) {
	t.Helper()
	p, err := newProxy(s)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(p)
	config := newServer(p)
	server.Config.ReadHeaderTimeout = config.ReadHeaderTimeout
	server.Config.ReadTimeout = config.ReadTimeout
	server.Config.IdleTimeout = config.IdleTimeout
	server.Config.MaxHeaderBytes = config.MaxHeaderBytes
	server.Config.ErrorLog = config.ErrorLog
	server.Start()
	t.Cleanup(func() { p.Close(); server.Close() })
	return p, server
}
func stubSettings(lookup *atomic.Int32) settings {
	return settings{maxTunnels: 1, lifetime: time.Second, idle: time.Second,
		resolve: func(context.Context, string) ([]netip.Addr, error) {
			lookup.Add(1)
			return nil, errors.New("synthetic-secret-error-must-not-escape")
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no synthetic upstream expected")
		}}
}
func TestStrictAuthorityMethodsAndNoConnectBody(t *testing.T) {
	var lookups atomic.Int32
	_, server := serve(t, stubSettings(&lookups))
	cases := []string{
		"GET http://graph.facebook.com/ HTTP/1.1\r\nHost: graph.facebook.com\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: graph.facebook.com:443\r\nContent-Length: 0\r\n\r\n",
	}
	for _, target := range []string{"graph.facebook.com", "graph.facebook.com:80", "graph.facebook.com:0443", "GRAPH.facebook.com:443", "graph.facebook.com.:443", "graph.facebook.com:443/", "graph.facebook.com:443?x=1", "graph.facebook.com:443#x", "user@graph.facebook.com:443", "https://graph.facebook.com:443", "graph.facebook.com.evil:443", "127.0.0.1:443", "[::1]:443", "graph.facebook.com%3a443"} {
		cases = append(cases, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	}
	for _, header := range []string{"Host: graph.facebook.com:443\r\nContent-Length: 0\r\n", "Host: graph.facebook.com:443\r\nContent-Length: 4\r\n", "Host: graph.facebook.com:443\r\nTransfer-Encoding: chunked\r\n", "Host: graph.facebook.com:443\r\nExpect: 100-continue\r\n", "Host: graph.facebook.com:443\r\nUpgrade: websocket\r\n"} {
		cases = append(cases, "CONNECT graph.facebook.com:443 HTTP/1.1\r\n"+header+"\r\n")
	}
	for i, raw := range cases {
		conn, _, res := request(t, server.URL, raw)
		if res.StatusCode < 400 || res.StatusCode >= 500 {
			t.Errorf("bad request case %d accepted: status %d", i, res.StatusCode)
		}
		conn.Close()
	}
	if lookups.Load() != 0 {
		t.Fatal("refused request reached resolver")
	}
}
func TestWireHostIgnoredCannotAlterPinnedDial(t *testing.T) {
	var lookups, dials atomic.Int32
	s := stubSettings(&lookups)
	s.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		lookups.Add(1)
		if host != "graph.facebook.com" {
			t.Error("wire Host selected DNS")
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	s.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if network != "tcp" || address != "8.8.8.8:443" {
			t.Error("wire Host selected upstream")
		}
		return nil, errors.New("synthetic dial failure")
	}
	_, server := serve(t, s)
	conn, _, res := request(t, server.URL, "CONNECT graph.facebook.com:443 HTTP/1.1\r\nHost: 169.254.169.254:80\r\n\r\n")
	defer conn.Close()
	if res.StatusCode != 502 || lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatal("unexpected standard CONNECT target behavior")
	}
}

func TestShutdownCancelsPendingDNSAndRawSocketBound(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	s := stubSettings(&calls)
	s.resolve = func(ctx context.Context, _ string) ([]netip.Addr, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p, server := serve(t, s)
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, connectRequest)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("DNS not reached")
	}
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel pending DNS")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := &boundedListener{Listener: listener, slots: make(chan struct{}, 2)}
	httpServer := newServer(http.NotFoundHandler())
	defer httpServer.Close()
	go httpServer.Serve(limited)
	sockets := []net.Conn{}
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		sockets = append(sockets, c)
		defer c.Close()
	}
	deadline := time.Now().Add(time.Second)
	for len(limited.slots) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(limited.slots) != 2 {
		t.Fatal("fixture did not fill raw slots")
	}
	extra, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	one := make([]byte, 1)
	if _, err = extra.Read(one); err == nil {
		t.Fatal("raw connection bound bypassed")
	} else if timed, ok := err.(net.Error); ok && timed.Timeout() {
		t.Fatal("excess raw socket remained open until client timeout")
	}
}

func TestSlowHeaderClosesBeforeClientDeadline(t *testing.T) {
	var lookups atomic.Int32
	p, err := newProxy(stubSettings(&lookups))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	server := httptest.NewUnstartedServer(p)
	server.Config.ReadHeaderTimeout = 75 * time.Millisecond
	server.Config.ReadTimeout = time.Second
	server.Start()
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(conn, "CONNECT graph.facebook.com:443 HTTP/1.1\r\nHost:")
	one := make([]byte, 1)
	if _, err = conn.Read(one); err == nil {
		t.Fatal("unfinished header returned data")
	} else if timed, ok := err.(net.Error); ok && timed.Timeout() {
		t.Fatal("header socket outlived header timeout")
	}
	if lookups.Load() != 0 {
		t.Fatal("unfinished header reached DNS")
	}
}

func TestResolutionAndDialDeadlineCancellationNoFallback(t *testing.T) {
	var lookups, dials atomic.Int32
	canceled := make(chan error, 1)
	s := stubSettings(&lookups)
	s.lifetime = 100 * time.Millisecond
	s.idle = 100 * time.Millisecond
	s.resolve = func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	s.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials.Add(1)
		<-ctx.Done()
		canceled <- ctx.Err()
		return nil, ctx.Err()
	}
	p, server := serve(t, s)
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	started := time.Now()
	io.WriteString(conn, connectRequest)
	var received bytes.Buffer
	response, err := http.ReadResponse(bufio.NewReader(io.TeeReader(io.LimitReader(conn, 4096), &received)), nil)
	// Parent cancellation closes the client in an AfterFunc, while the dial
	// error path may write a generic 502 first. Either order is valid. Merely
	// receiving an HTTP error byte is not a successful CONNECT. If close wins
	// during a write, only a prefix of that exact error response is acceptable.
	const rejected = "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
	if !strings.HasPrefix(rejected, received.String()) || err == nil && response.StatusCode != http.StatusBadGateway {
		t.Fatalf("deadline produced an unexpected response: %q", received.String())
	}
	if timed, ok := err.(net.Error); ok && timed.Timeout() {
		t.Fatal("dial was not canceled within attempt lifetime")
	}
	p.Close()
	select {
	case cause := <-canceled:
		if !errors.Is(cause, context.DeadlineExceeded) {
			t.Fatal("dial did not observe its deadline")
		}
	default:
		t.Fatal("dial cancellation was never observed")
	}
	if time.Since(started) > time.Second {
		t.Fatal("dial timeout exit exceeded bounded fixture allowance")
	}
	if lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatal("deadline caused fallback")
	}
}
func TestPublicAddressPolicyAndMixedAnswerRefusal(t *testing.T) {
	for _, value := range []string{"0.0.0.0", "0.1.2.3", "10.0.0.1", "100.100.100.200", "127.0.0.1", "169.254.169.254", "172.31.1.1", "192.168.1.1", "192.0.0.9", "192.0.2.1", "192.88.99.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255", "168.63.129.16", "::", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1%eth0", "ff02::1", "64:ff9b::a00:1", "2001:db8::1", "2001::1", "2002:a00:1::1", "3fff::1"} {
		if publicIP(netip.MustParseAddr(value)) {
			t.Errorf("nonpublic/reserved allowed: %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "::ffff:8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(value)) {
			t.Errorf("public policy refused: %s", value)
		}
	}
	var calls, dials atomic.Int32
	s := stubSettings(&calls)
	s.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")}, nil
	}
	s.dial = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("must not dial")
	}
	_, server := serve(t, s)
	conn, _, res := request(t, server.URL, connectRequest)
	defer conn.Close()
	if res.StatusCode != 403 || dials.Load() != 0 {
		t.Fatal("mixed DNS answer escaped checked set")
	}
}
func TestNoDNSRebindNoDialRetryAndNoSensitiveError(t *testing.T) {
	var lookups, dials atomic.Int32
	s := stubSettings(&lookups)
	s.resolve = func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}, nil
	}
	s.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if network != "tcp" || address != "8.8.8.8:443" {
			t.Error("not a pinned literal")
		}
		return nil, errors.New("private-provider-token-fixture")
	}
	_, server := serve(t, s)
	conn, reader, res := request(t, server.URL, connectRequest)
	defer conn.Close()
	if res.StatusCode != 502 || lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatal("unapproved fallback/rebind", res.StatusCode)
	}
	body, _ := io.ReadAll(reader)
	if strings.Contains(string(body), "private-provider-token-fixture") {
		t.Fatal("dial error leaked")
	}
}
func TestOversizedHeadersAndHeaderReadTimeout(t *testing.T) {
	var lookups atomic.Int32
	_, server := serve(t, stubSettings(&lookups))
	conn, _, res := request(t, server.URL, strings.TrimSuffix(connectRequest, "\r\n")+"X-Noise: "+strings.Repeat("x", 65536)+"\r\n\r\n")
	if res.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatal("header bound", res.StatusCode)
	}
	conn.Close()
	if lookups.Load() != 0 {
		t.Fatal("oversized request resolved")
	}
	config := newServer(http.NotFoundHandler())
	if config.ReadHeaderTimeout <= 0 || config.ReadTimeout <= 0 || config.IdleTimeout <= 0 || config.MaxHeaderBytes > headerLimit {
		t.Fatal("missing header/body parser bounds")
	}
}

func TestActualRawHeaderParserReadAheadBound(t *testing.T) {
	var lookups atomic.Int32
	_, server := serve(t, stubSettings(&lookups))
	// The handler counts parsed request metadata separately. The Go server's
	// raw request parser has bounded read-ahead beyond MaxHeaderBytes; exercise
	// both sides rather than describing the configured value as a wire limit.
	for _, padding := range []int{7800, 8100, 8192, 11000, 12000, 12203, 12204, 12205, 12300, 13000} {
		raw := strings.TrimSuffix(connectRequest, "\r\n") + "X-Noise: " + strings.Repeat("x", padding) + "\r\n\r\n"
		before := lookups.Load()
		conn, _, result := request(t, server.URL, raw)
		conn.Close()
		t.Logf("raw request bytes=%d status=%d reached-resolver=%t", len(raw), result.StatusCode, lookups.Load() != before)
		switch {
		case padding == 7800:
			if result.StatusCode != 502 || lookups.Load() != before+1 {
				t.Fatal("valid below-limit fixture did not reach handler")
			}
		case len(raw) > headerLimit+4096:
			if result.StatusCode != http.StatusRequestHeaderFieldsTooLarge || lookups.Load() != before {
				t.Fatal("raw parser hard bound not enforced")
			}
		case padding >= 8192:
			if result.StatusCode != 400 || lookups.Load() != before {
				t.Fatal("parsed metadata bound not enforced before DNS")
			}
		}
	}
	// Whitespace is normalized by Go before the handler's metadata count. This
	// exercises the parser's wire bound even when the parsed limit is not hit.
	for _, padding := range []int{12204, 12205} {
		raw := strings.TrimSuffix(connectRequest, "\r\n") + "X-Noise: " + strings.Repeat(" ", padding) + "\r\n\r\n"
		before := lookups.Load()
		conn, _, result := request(t, server.URL, raw)
		conn.Close()
		t.Logf("normalized-whitespace raw request bytes=%d status=%d reached-resolver=%t", len(raw), result.StatusCode, lookups.Load() != before)
		if len(raw) == headerLimit+4096 {
			if result.StatusCode != 502 || lookups.Load() != before+1 {
				t.Fatal("normalized fixture did not reach parser wire boundary")
			}
		} else if result.StatusCode != 431 || lookups.Load() != before {
			t.Fatal("normalized raw header exceeded parser hard bound")
		}
	}
}
func TestBufferedSecondRequestIsNotAConnectBody(t *testing.T) {
	var lookups atomic.Int32
	_, server := serve(t, stubSettings(&lookups))
	conn, _, res := request(t, server.URL, connectRequest+"GET /private-token-fixture HTTP/1.1\r\n\r\n")
	defer conn.Close()
	if res.StatusCode != 400 || lookups.Load() != 0 {
		t.Fatal("buffered replay/body forwarded")
	}
}
func TestConcurrencyIdleLifetimeAndShutdown(t *testing.T) {
	for _, kind := range []string{"idle", "lifetime", "shutdown"} {
		t.Run(kind, func(t *testing.T) {
			var dials atomic.Int32
			s := settings{maxTunnels: 1, lifetime: 350 * time.Millisecond, idle: 100 * time.Millisecond,
				resolve: func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				},
				dial: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					a, b := net.Pipe()
					go func() { defer b.Close(); io.Copy(b, b) }()
					return a, nil
				}}
			p, server := serve(t, s)
			conn, reader, res := request(t, server.URL, connectRequest)
			defer conn.Close()
			if res.StatusCode != 200 {
				t.Fatal(res.StatusCode)
			}
			second, _, busy := request(t, server.URL, connectRequest)
			second.Close()
			if busy.StatusCode != 503 || dials.Load() != 1 {
				t.Fatal("capacity reached upstream")
			}
			started := time.Now()
			if kind == "shutdown" {
				p.Close()
			} else if kind == "lifetime" {
				for {
					if _, err := conn.Write([]byte("x")); err != nil {
						break
					}
					one := make([]byte, 1)
					if _, err := io.ReadFull(reader, one); err != nil {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
			_, err := reader.ReadByte()
			if err == nil {
				t.Fatal("tunnel was not closed")
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("unbounded tunnel exit")
			}
			if kind == "shutdown" {
				last, _, closed := request(t, server.URL, connectRequest)
				last.Close()
				if closed.StatusCode != 503 {
					t.Fatal("new tunnel after shutdown")
				}
			}
		})
	}
}
func TestListenAndSettingsRefuseImplicitOrUnboundedValues(t *testing.T) {
	for _, address := range []string{":8080", "localhost:8080", "127.0.0.1:0", "127.0.0.1:08080", "127.0.0.1:65536", "[fe80::1%eth0]:8080"} {
		if listenAddress(address) == nil {
			t.Errorf("ambiguous listen accepted: %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:18081", "0.0.0.0:18081", "[::1]:18081"} {
		if listenAddress(address) != nil {
			t.Errorf("explicit listen refused: %s", address)
		}
	}
	for _, s := range []settings{{maxTunnels: 0, lifetime: time.Second, idle: time.Second}, {maxTunnels: 65, lifetime: time.Second, idle: time.Second}, {maxTunnels: 1, lifetime: 11 * time.Minute, idle: time.Second}, {maxTunnels: 1, lifetime: time.Second, idle: 2 * time.Second}} {
		if p, err := newProxy(s); err == nil {
			p.Close()
			t.Fatal("unbounded settings accepted")
		}
	}
}
