//go:build linux

// extensions-schema is an offline schema-owner tool. It never starts a runtime.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/v2"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type source []byte

func (s source) ReadBytes() ([]byte, error)            { return s, nil }
func (s source) Read() (map[string]interface{}, error) { return nil, errors.New("bytes required") }
func privateConfig(path string) (*config.Config, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) {
		return nil, protectedschema.ErrDenied
	}
	before, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > 1<<20 {
		return nil, protectedschema.ErrDenied
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		return nil, protectedschema.ErrDenied
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return nil, protectedschema.ErrDenied
	}
	k := koanf.New(".")
	if e = k.Load(source(b), toml.Parser()); e != nil {
		return nil, e
	}
	var c config.Config
	if e = k.Unmarshal("", &c); e != nil {
		return nil, e
	}
	return &c, nil
}
func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("extensions-schema", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "root-private schema-owner TOML; environment overrides ignored")
	baseline := flags.Bool("baseline", false, "derive reference catalog in a NEW disposable empty database")
	install := flags.Bool("install", false, "explicit one-shot migration/seal of an isolated protected database")
	org := flags.String("organization-id", "", "exact existing protected organization for -install")
	output := flags.String("catalog-output", "", "NEW root-private canonical catalog output path")
	if e := flags.Parse(args); e != nil || flags.NArg() != 0 || *baseline == *install || !filepath.IsAbs(*output) {
		return protectedschema.ErrDenied
	}
	c, e := privateConfig(*path)
	if e != nil {
		return e
	}
	if protectedschema.ValidateInstallation(c, *baseline) != nil {
		return protectedschema.ErrDenied
	}
	if c.Database.Host == "" || c.Database.Name == "" || c.Database.User == "" || c.Database.Password == "" || c.Database.Port < 1 || c.Database.Port > 65535 || c.Database.SSLMode != "verify-full" {
		return protectedschema.ErrDenied
	}
	// Reserve review output before any owner operation. Existing/partial output
	// refuses the operation; unknown results require inspection, never retry.
	for parent := filepath.Dir(*output); ; parent = filepath.Dir(parent) {
		info, e := os.Lstat(parent)
		if e != nil {
			return e
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return protectedschema.ErrDenied
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	file, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer file.Close()
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.Database.User, c.Database.Password), Host: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Path: "/" + c.Database.Name}
	q := url.Values{"sslmode": []string{"verify-full"}, "connect_timeout": []string{"10"}, "search_path": []string{"public"}}
	u.RawQuery = q.Encode()
	db, e := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		return protectedschema.ErrDenied
	}
	pool, e := db.DB()
	if e != nil {
		return e
	}
	defer pool.Close()
	pool.SetMaxOpenConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	manifest, e := protectedschema.Install(ctx, db, c, *org, *baseline)
	if e != nil {
		return e
	}
	if e = json.NewEncoder(file).Encode(manifest); e != nil {
		return e
	}
	if e = file.Sync(); e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(map[string]any{"release": manifest.Release, "catalog_sha256": manifest.SHA256, "baseline_only": *baseline, "sealed": *install, "runtime_started": false, "dispatch_held": true})
}
func main() {
	if e := run(os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "extensions-schema: refused or unknown; keep database isolated, inspect privately, do not retry")
		os.Exit(1)
	}
}
