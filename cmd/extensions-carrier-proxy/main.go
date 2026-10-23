// extensions-carrier-proxy is an opaque, destination-constrained CONNECT proxy.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run(args []string) error {
	flags := flag.NewFlagSet("extensions-carrier-proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "127.0.0.1:18081", "explicit literal IP:port; isolate this listener from other tenants")
	max := flags.Int("max-tunnels", 4, "maximum active or resolving tunnels (1..64)")
	lifetime := flags.Duration("tunnel-lifetime", 2*time.Minute, "absolute tunnel lifetime, at most 10m")
	idle := flags.Duration("idle-timeout", 30*time.Second, "maximum inactivity, no greater than tunnel lifetime")
	if flags.Parse(args) != nil || flags.NArg() != 0 || listenAddress(*listen) != nil {
		return refused
	}
	p, err := newProxy(settings{maxTunnels: *max, lifetime: *lifetime, idle: *idle})
	if err != nil {
		return refused
	}
	defer p.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return refused
	}
	defer listener.Close()
	server := newServer(p)
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bounded := &boundedListener{Listener: listener, slots: make(chan struct{}, 2*(*max))}
	done := make(chan error, 1)
	go func() { done <- server.Serve(bounded) }()
	select {
	case err = <-done:
		if err != http.ErrServerClosed {
			return refused
		}
	case <-signalCtx.Done():
		p.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
			return refused
		}
		if <-done != http.ErrServerClosed {
			return refused
		}
	}
	return nil
}
func main() {
	if run(os.Args[1:]) != nil {
		fmt.Fprintln(os.Stderr, refused)
		os.Exit(1)
	}
}
