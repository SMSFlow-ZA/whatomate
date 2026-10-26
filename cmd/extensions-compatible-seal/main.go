// extensions-compatible-seal admits a new compiled release only when the actual
// protected catalog is already identical. It never runs schema migrations.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/v2"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

var refused = errors.New("compatible source seal refused or unknown; keep isolated and inspect; do not retry")

type configBytes []byte

func (b configBytes) ReadBytes() ([]byte, error)            { return b, nil }
func (b configBytes) Read() (map[string]interface{}, error) { return nil, refused }

func readConfig(path string) (*config.Config, error) {
	raw, e := readPrivate(path)
	if e != nil {
		return nil, refused
	}
	k := koanf.New(".")
	if k.Load(configBytes(raw), toml.Parser()) != nil {
		return nil, refused
	}
	c := &config.Config{}
	if k.Unmarshal("", c) != nil {
		return nil, refused
	}
	return c, nil
}

func connection(c *config.Config, pem []byte) (*pgx.ConnConfig, error) {
	if c == nil || c.Database.Host == "" || c.Database.Password == "" || c.Database.Port < 1 || c.Database.Port > 65535 || c.Database.SSLMode != "verify-full" {
		return nil, refused
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, refused
	}
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Path: "/" + c.Database.Name, User: url.UserPassword(c.Database.User, c.Database.Password)}
	u.RawQuery = "sslmode=verify-full"
	p, e := pgx.ParseConfig(u.String())
	if e != nil {
		return nil, refused
	}
	p.Host = c.Database.Host
	p.Port = uint16(c.Database.Port)
	p.Database = c.Database.Name
	p.User = c.Database.User
	p.Password = c.Database.Password
	p.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: c.Database.Host}
	p.Fallbacks = nil
	p.ConnectTimeout = 5 * time.Second
	p.RuntimeParams = map[string]string{"search_path": "public", "application_name": "extensions-compatible-seal", "statement_timeout": "25000", "lock_timeout": "3000"}
	return p, nil
}

func run(args []string, out io.Writer) error {
	f := flag.NewFlagSet("extensions-compatible-seal", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configPath := f.String("config", "", "root-private held schema-owner TOML")
	caPath := f.String("ca-file", "", "root-private database CA PEM")
	previous := f.String("previous-release", "", "exact current compiled release identity")
	generation := f.Int64("previous-generation", 0, "exact current seal generation")
	organization := f.String("organization-id", "", "exact protected organization")
	output := f.String("catalog-output", "", "NEW private physical catalog output; apply only")
	apply := f.Bool("apply", false, "reserve durable attempt and seal; default readonly validation")
	if f.Parse(args) != nil || f.NArg() != 0 || *configPath == "" || *caPath == "" || *output == "" {
		return refused
	}
	for _, item := range os.Environ() {
		if strings.HasPrefix(strings.SplitN(item, "=", 2)[0], "PG") {
			return refused
		}
	}
	c, e := readConfig(*configPath)
	if e != nil {
		return refused
	}
	r := protectedschema.CompatibleRequest{PreviousRelease: *previous, PreviousGeneration: *generation, OrganizationID: *organization}
	if protectedschema.ValidateCompatible(c, r) != nil {
		return refused
	}
	pem, e := readPrivate(*caPath)
	if e != nil {
		return refused
	}
	p, e := connection(c, pem)
	if e != nil {
		return refused
	}
	// An empty reserved file survives every unknown DB outcome. Never overwrite
	// it or remove it as an automatic retry mechanism.
	var catalog *os.File
	if *apply {
		catalog, e = reserveOutput(*output)
		if e != nil {
			return refused
		}
		defer catalog.Close()
	} else if validateOutput(*output) != nil {
		return refused
	}
	db := stdlib.OpenDB(*p)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	manifest, result, e := protectedschema.SealCompatible(ctx, db, c, r, *apply)
	if e != nil {
		return refused
	}
	if *apply {
		if e = json.NewEncoder(catalog).Encode(manifest); e != nil {
			return refused
		}
		if e = catalog.Sync(); e != nil {
			return refused
		}
		if finishOutput(*output, catalog) != nil {
			return refused
		}
	}
	return json.NewEncoder(out).Encode(result)
}

func main() {
	if run(os.Args[1:], os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, "extensions-compatible-seal: refused or unknown; keep isolated and inspect; do not retry")
		os.Exit(1)
	}
}
