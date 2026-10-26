package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
)

func TestConnectionPinsOwnerTLSAndIgnoresFallbacks(t *testing.T) {
	public, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-compatible-test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if e != nil {
		t.Fatal(e)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	c := &config.Config{}
	c.Database.Host = "db.example.invalid"
	c.Database.Port = 5432
	c.Database.User = "fixture_owner"
	c.Database.Name = "fixture"
	c.Database.Password = "synthetic:@/password"
	c.Database.SSLMode = "verify-full"
	t.Setenv("PGHOST", "untrusted.invalid")
	p, e := connection(c, ca)
	if e != nil {
		t.Fatal(e)
	}
	if p.Host != c.Database.Host || p.User != c.Database.User || p.Password != c.Database.Password || p.Database != c.Database.Name || p.Fallbacks != nil || p.TLSConfig.InsecureSkipVerify || p.TLSConfig.MinVersion < tls.VersionTLS12 || p.TLSConfig.ServerName != c.Database.Host || p.RuntimeParams["search_path"] != "public" {
		t.Fatal("explicit owner TLS contract changed")
	}
	c.Database.SSLMode = "disable"
	if _, e = connection(c, ca); e == nil {
		t.Fatal("plaintext accepted")
	}
	c.Database.SSLMode = "verify-full"
	if _, e = connection(c, []byte("not a CA")); e == nil {
		t.Fatal("untrusted CA accepted")
	}
}

func TestCommandRefusesAmbientPGAndNeverPrintsCredentials(t *testing.T) {
	t.Setenv("PGSERVICE", "synthetic-untrusted-service")
	var output bytes.Buffer
	err := run([]string{"-config", "/root/nonexistent-owner", "-ca-file", "/root/nonexistent-ca", "-catalog-output", "/root/new-catalog"}, &output)
	if err == nil || output.Len() != 0 {
		t.Fatal("ambient PG defaults or private diagnostics accepted")
	}
}

func TestCommandMissingInputsRefuseBeforeConnection(t *testing.T) {
	for _, args := range [][]string{{}, {"-apply"}, {"-previous-generation", "-1"}, {"-unknown"}} {
		var output bytes.Buffer
		if run(args, &output) == nil || output.Len() != 0 {
			t.Fatal("incomplete invocation accepted")
		}
	}
}
