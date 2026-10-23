package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestEndToEndTLSIsClientVerifiedThroughOpaqueTunnel(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic graph fixture"}, DNSNames: []string{"graph.facebook.com"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	for _, hostname := range []string{"graph.facebook.com", "wrong.invalid"} {
		t.Run(hostname, func(t *testing.T) {
			finished := make(chan struct{})
			_, server := serve(t, settings{maxTunnels: 1, lifetime: 2 * time.Second, idle: time.Second,
				resolve: func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				},
				dial: func(_ context.Context, _, address string) (net.Conn, error) {
					if address != "8.8.8.8:443" {
						t.Error("TLS dial not pinned")
					}
					a, b := net.Pipe()
					go func() {
						defer close(finished)
						defer b.Close()
						secure := tls.Server(b, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}}})
						defer secure.Close()
						b.SetDeadline(time.Now().Add(2 * time.Second))
						if secure.Handshake() != nil {
							return
						}
						data := make([]byte, 4)
						if _, err := io.ReadFull(secure, data); err == nil {
							secure.Write(data)
						}
					}()
					return a, nil
				}})
			conn, _, response := request(t, server.URL, connectRequest)
			defer conn.Close()
			if response.StatusCode != 200 {
				t.Fatal(response.StatusCode)
			}
			client := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: hostname})
			defer client.Close()
			err := client.Handshake()
			if hostname != "graph.facebook.com" {
				if err == nil {
					t.Fatal("client accepted wrong upstream hostname")
				}
				return
			}
			if err != nil {
				t.Fatal("end-to-end TLS", err)
			}
			if _, err = client.Write([]byte("DATA")); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 4)
			if _, err = io.ReadFull(client, data); err != nil || string(data) != "DATA" {
				t.Fatal("encrypted echo", err)
			}
			client.Close()
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("synthetic TLS peer not closed")
			}
		})
	}
}
