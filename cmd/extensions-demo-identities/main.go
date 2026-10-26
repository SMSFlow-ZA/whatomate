// extensions-demo-identities creates six unprivileged synthetic acceptance identities offline.
// Authorization is a separate existing extensions-admin operation. No publication occurs here.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/v2"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type rawConfig []byte

func (r rawConfig) ReadBytes() ([]byte, error)            { return r, nil }
func (r rawConfig) Read() (map[string]interface{}, error) { return nil, refused }
func readConfig(path string) (*config.Config, error) {
	raw, e := readPrivate(path)
	if e != nil {
		return nil, refused
	}
	k := koanf.New(".")
	if k.Load(rawConfig(raw), toml.Parser()) != nil {
		return nil, refused
	}
	c := &config.Config{}
	if k.Unmarshal("", c) != nil {
		return nil, refused
	}
	return c, nil
}

func run(args []string, out io.Writer) error {
	f := flag.NewFlagSet("extensions-demo-identities", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configPath := f.String("config", "", "root-private owner TOML")
	planPath := f.String("plan", "", "exact root-private six-fixture identity plan")
	caPath := f.String("ca-file", "", "root-private database CA")
	attempt := f.String("attempt", "", "create-only attempt JSON")
	passwordPath := f.String("password-out", "", "create-only private fixture-password JSON beside attempt")
	reconcileOnly := f.Bool("reconcile", false, "read-only exact committed-state reconciliation before any grants")
	apply := f.Bool("apply", false, "create identity; omitted means readonly preview")
	if f.Parse(args) != nil || *apply && *reconcileOnly || f.NArg() != 0 || *configPath == "" || *planPath == "" || *caPath == "" || *attempt == "" || *passwordPath == "" || *attempt == *passwordPath || filepath.Dir(*attempt) != filepath.Dir(*passwordPath) {
		return refused
	}
	for _, item := range os.Environ() {
		if strings.HasPrefix(strings.SplitN(item, "=", 2)[0], "PG") {
			return refused
		}
	}
	c, e := readConfig(*configPath)
	if e != nil || protectedschema.Validate(c) != nil || c.App.Environment != "production" || c.App.Debug || c.Database.SSLMode != "verify-full" || c.Database.Password == "" || c.Database.Host == "" || c.Database.Port < 1 || c.Database.Port > 65535 {
		return refused
	}
	raw, e := readPrivate(*planPath)
	if e != nil {
		return refused
	}
	p, e := decodePlan(raw)
	if e != nil {
		return refused
	}
	pem, e := readPrivate(*caPath)
	if e != nil {
		return refused
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return refused
	}
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Path: "/" + c.Database.Name, User: url.UserPassword(c.Database.User, c.Database.Password)}
	u.RawQuery = "sslmode=verify-full"
	pg, e := pgx.ParseConfig(u.String())
	if e != nil {
		return refused
	}
	pg.Host = c.Database.Host
	pg.Port = uint16(c.Database.Port)
	pg.User = c.Database.User
	pg.Password = c.Database.Password
	pg.Database = c.Database.Name
	pg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: c.Database.Host}
	pg.Fallbacks = nil
	pg.RuntimeParams = map[string]string{"search_path": "public", "application_name": "extensions-demo-identities", "lock_timeout": "3000", "statement_timeout": "25000"}
	pg.ConnectTimeout = 5 * time.Second
	db := stdlib.OpenDB(*pg)
	defer db.Close()
	db.SetMaxOpenConns(3)
	planSHA := fmt.Sprintf("%x", sha256.Sum256(raw))
	passwords := map[string][]byte{}
	if *reconcileOnly {
		secret, e := readPrivate(*passwordPath)
		if e != nil {
			return refused
		}
		var stored map[string]string
		if json.Unmarshal(secret, &stored) != nil {
			return refused
		}
		for label, value := range stored {
			passwords[label] = []byte(value)
		}
		if len(passwords) != len(fixtureLabels) {
			return refused
		}
	}
	if *reconcileOnly {
		priorRaw, e := readPrivate(*attempt)
		if e != nil {
			return refused
		}
		var prior Result
		if json.Unmarshal(priorRaw, &prior) != nil {
			return refused
		}
		expected := resultFor(p, planSHA, "reserved_inspect_database_before_reconciliation")
		actualJSON, _ := json.Marshal(prior)
		expectedJSON, _ := json.Marshal(expected)
		if !bytes.Equal(actualJSON, expectedJSON) {
			return refused
		}
	}
	if *apply {
		if reserveAttempt(*attempt, resultFor(p, planSHA, "reserved")) != nil {
			return refused
		}
		for _, identity := range p.Identities {
			entropy := make([]byte, 32)
			if _, e = rand.Read(entropy); e != nil {
				return refused
			}
			passwords[identity.Fixture] = []byte(base64.RawURLEncoding.EncodeToString(entropy))
		}
		stored := map[string]string{}
		for label, value := range passwords {
			stored[label] = string(value)
		}
		secret, e := json.Marshal(stored)
		if e != nil || writePassword(*passwordPath, secret) != nil {
			return refused
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result Result
	if *reconcileOnly {
		result, e = reconcile(ctx, db, c, p, "public", passwords, planSHA)
	} else {
		result, e = provision(ctx, db, c, p, "public", passwords, *apply, planSHA)
	}
	if e != nil {
		_ = json.NewEncoder(out).Encode(result)
		return refused
	}
	return json.NewEncoder(out).Encode(result)
}
func main() {
	if run(os.Args[1:], os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, refused)
		os.Exit(1)
	}
}
