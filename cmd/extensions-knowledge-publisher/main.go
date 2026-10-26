// extensions-knowledge-publisher creates one unprivileged synthetic automation identity offline.
// Authorization is a separate existing extensions-admin operation. No publication occurs here.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
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
func decodePlan(raw []byte) (Plan, error) {
	check := json.NewDecoder(bytes.NewReader(raw))
	token, e := check.Token()
	if e != nil || token != json.Delim('{') {
		return Plan{}, refused
	}
	seen := map[string]bool{}
	for check.More() {
		key, e := check.Token()
		name, ok := key.(string)
		if e != nil || !ok || seen[name] {
			return Plan{}, refused
		}
		seen[name] = true
		var value json.RawMessage
		if check.Decode(&value) != nil {
			return Plan{}, refused
		}
	}
	var p Plan
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return p, refused
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, refused
	}
	return p, validPlan(p)
}

func run(args []string, out io.Writer) error {
	f := flag.NewFlagSet("extensions-knowledge-publisher", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configPath := f.String("config", "", "root-private owner TOML")
	planPath := f.String("plan", "", "exact root-private synthetic identity plan")
	caPath := f.String("ca-file", "", "root-private database CA")
	attempt := f.String("attempt", "", "create-only attempt JSON")
	passwordPath := f.String("password-out", "", "create-only private password file beside attempt")
	apply := f.Bool("apply", false, "create identity; omitted means readonly preview")
	if f.Parse(args) != nil || f.NArg() != 0 || *configPath == "" || *planPath == "" || *caPath == "" || *attempt == "" || *passwordPath == "" || *attempt == *passwordPath || filepath.Dir(*attempt) != filepath.Dir(*passwordPath) {
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
	pg.RuntimeParams = map[string]string{"search_path": "public", "application_name": "extensions-knowledge-publisher", "lock_timeout": "3000", "statement_timeout": "25000"}
	pg.ConnectTimeout = 5 * time.Second
	db := stdlib.OpenDB(*pg)
	defer db.Close()
	db.SetMaxOpenConns(3)
	var password []byte
	if *apply {
		if reserveAttempt(*attempt, Result{OrganizationID: p.OrganizationID, UserID: p.UserID, AutomationSynthetic: true, PrimaryOnly: true}) != nil {
			return refused
		}
		entropy := make([]byte, 32)
		if _, e = rand.Read(entropy); e != nil {
			return refused
		}
		password = []byte(base64.RawURLEncoding.EncodeToString(entropy))
		if writePassword(*passwordPath, password) != nil {
			return refused
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, e := provision(ctx, db, c, p, "public", password, *apply)
	if e != nil {
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
