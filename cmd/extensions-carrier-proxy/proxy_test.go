package main

import (
	"bufio"
	"context"
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

func TestPinnedConnectOpaqueAndSingleLookup(t *testing.T) {
	var lookups, dials atomic.Int32
	p, err := newProxy(settings{maxTunnels: 1, lifetime: time.Second, idle: time.Second, resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
		lookups.Add(1)
		if host != "graph.facebook.com" {
			t.Error("wrong resolver name")
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, dial: func(_ context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if network != "tcp" || address != "8.8.8.8:443" {
			t.Error("unpinned dial")
		}
		client, upstream := net.Pipe()
		go func() {
			defer upstream.Close()
			b := make([]byte, 4)
			if _, err := io.ReadFull(upstream, b); err == nil {
				upstream.Write(b)
			}
		}()
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	server := httptest.NewServer(p)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(conn, "CONNECT graph.facebook.com:443 HTTP/1.1\r\nHost: graph.facebook.com:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 200 {
		t.Fatal("connect", err, response)
	}
	conn.Write([]byte("PING"))
	echo := make([]byte, 4)
	if _, err = io.ReadFull(reader, echo); err != nil || string(echo) != "PING" {
		t.Fatal("opaque tunnel", err)
	}
	if lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatal("retried DNS or dial")
	}
}
