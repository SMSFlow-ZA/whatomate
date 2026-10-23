package protectedschema

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/config"
)

func TestOpenRuntimeConfigRejectsDifferentConnectionIdentityWithoutDial(t *testing.T) {
	release, shape := ReleaseIdentity, ExpectedCatalogSHA256
	t.Cleanup(func() { ReleaseIdentity, ExpectedCatalogSHA256 = release, shape })
	ReleaseIdentity, ExpectedCatalogSHA256 = strings.Repeat("a", 40)+":"+strings.Repeat("b", 40), strings.Repeat("c", 64)
	c := &config.Config{}
	c.Extensions.ProtectedMode = true
	c.Database = config.DatabaseConfig{SchemaManagedExternally: true, SchemaRelease: ReleaseIdentity, SchemaSHA256: ExpectedCatalogSHA256, Host: "invalid.invalid", Port: 5432, Name: "db", User: "worker", Password: "synthetic"}
	pc, err := pgx.ParseConfig("postgres://worker:synthetic@invalid.invalid:5432/db?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"host", "port", "database", "user", "password"} {
		bad := pc.Copy()
		switch field {
		case "host":
			bad.Host = "other.invalid"
		case "port":
			bad.Port++
		case "database":
			bad.Database = "other"
		case "user":
			bad.User = "other"
		case "password":
			bad.Password = "other"
		}
		if db, e := OpenRuntimeConfig(c, bad); e == nil {
			pool, _ := db.DB()
			pool.Close()
			t.Fatal("different identity accepted", field)
		}
	}
	if _, err = OpenRuntimeConfig(c, nil); err == nil {
		t.Fatal("nil connection accepted")
	}
}

func TestRuntimeConfigCopiesExplicitTLSAndCertificatePools(t *testing.T) {
	pc, err := pgx.ParseConfig("postgres://worker:synthetic@invalid.invalid:5432/db?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	pc.TLSConfig = &tls.Config{RootCAs: x509.NewCertPool(), ClientCAs: x509.NewCertPool(), ServerName: "invalid.invalid", MinVersion: tls.VersionTLS12}
	pc.Fallbacks = []*pgconn.FallbackConfig{{Host: "backup.invalid", Port: 5432, TLSConfig: &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "backup.invalid"}}}
	copy := copyRuntimeConfig(pc)
	if copy == pc || copy.TLSConfig == pc.TLSConfig || copy.TLSConfig.RootCAs == pc.TLSConfig.RootCAs || copy.TLSConfig.ClientCAs == pc.TLSConfig.ClientCAs {
		t.Fatal("trusted TLS policy retained mutable caller ownership")
	}
	if copy.Fallbacks[0] == pc.Fallbacks[0] || copy.Fallbacks[0].TLSConfig == pc.Fallbacks[0].TLSConfig || copy.Fallbacks[0].TLSConfig.RootCAs == pc.Fallbacks[0].TLSConfig.RootCAs {
		t.Fatal("fallback settings retained mutable caller ownership")
	}
	pc.TLSConfig.ServerName = "other.invalid"
	pc.RuntimeParams["search_path"] = "other"
	pc.Fallbacks[0].Host = "changed.invalid"
	pc.Fallbacks[0].TLSConfig.ServerName = "changed.invalid"
	pc.Fallbacks[0].TLSConfig.RootCAs = nil
	if copy.TLSConfig.ServerName != "invalid.invalid" || copy.TLSConfig.InsecureSkipVerify || copy.TLSConfig.MinVersion != tls.VersionTLS12 || copy.RuntimeParams["search_path"] != "" {
		t.Fatal("parsed TLS/runtime parameters changed")
	}
	if copy.Fallbacks[0].Host != "backup.invalid" || copy.Fallbacks[0].TLSConfig.ServerName != "backup.invalid" || copy.Fallbacks[0].TLSConfig.RootCAs == nil {
		t.Fatal("caller fallback mutation changed copied policy")
	}
}
