// extensions-carrier-account provisions an inactive account offline; no Meta calls.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/v2"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
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
	f := flag.NewFlagSet("extensions-carrier-account", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	conf := f.String("config", "", "root-private owner TOML")
	plan := f.String("plan", "", "root-private exact account JSON")
	attempt := f.String("attempt", "", "create-only root-private attempt path")
	ca := f.String("ca-file", "", "root-private database CA PEM")
	apply := f.Bool("apply", false, "commit inactive account, default preview")
	activation := f.Bool("activate", false, "select exact inactive-to-active owner operation; default preview unless -apply")
	if f.Parse(args) != nil || f.NArg() != 0 || *conf == "" || *plan == "" || *attempt == "" || *ca == "" {
		return refused
	}
	c, e := readConfig(*conf)
	if e != nil || protectedschema.Validate(c) != nil || c.App.Environment != "production" || c.App.Debug || c.Extensions.CarrierEnabled || c.Extensions.CarrierDispatchEnabled || c.Extensions.SyntheticDevelopment || len(c.App.EncryptionKey) < 32 || !c.Channels.StopDispatch {
		return refused
	}
	if c.Database.Host == "" || c.Database.Port < 1 || c.Database.Port > 65535 || c.Database.User == "" || c.Database.Password == "" || c.Database.Name == "" || c.Database.SSLMode != "verify-full" {
		return refused
	}
	data, e := readPrivate(*plan)
	if e != nil {
		return refused
	}
	p, e := decodePlan(data)
	if e != nil {
		return refused
	}
	pem, e := readPrivate(*ca)
	if e != nil {
		return refused
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return refused
	}
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Path: "/" + c.Database.Name, User: url.UserPassword(c.Database.User, c.Database.Password)}
	q := url.Values{"sslmode": {"verify-full"}}
	u.RawQuery = q.Encode()
	pg, e := pgx.ParseConfig(u.String())
	if e != nil {
		return refused
	}
	// Explicit credentials, TLS roots, runtime parameters and no fallbacks defeat
	// PG environment/service-file defaults as well as host config env overrides.
	pg.Host = c.Database.Host
	pg.Port = uint16(c.Database.Port)
	pg.User = c.Database.User
	pg.Password = c.Database.Password
	pg.Database = c.Database.Name
	pg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: c.Database.Host}
	pg.Fallbacks = nil
	pg.RuntimeParams = map[string]string{"search_path": "public", "application_name": "extensions-carrier-account"}
	pg.ConnectTimeout = 5 * time.Second
	db := stdlib.OpenDB(*pg)
	defer db.Close()
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operation := provision
	if *activation {
		operation = activate
	}
	r, e := operation(ctx, db, c, p, "public", *attempt, *apply)
	if e != nil {
		return refused
	}
	return json.NewEncoder(out).Encode(r)
}
func main() {
	if run(os.Args[1:], os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, refused)
		os.Exit(1)
	}
}
