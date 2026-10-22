//go:build linux

// extensions-processing is the separate trusted production document controller.
// Only this process receives a Docker credential and dedicated queue DB role.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/processing"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var denied = errors.New("production document controller unavailable; inspect private owner state")

type configuration struct {
	InstanceID, OrganizationID, WorkerUserID, ProfileID       string
	InstanceRoot, DatabaseName, DatabaseRole, DatabaseDSNFile string
	Audiences                                                 []string
	DefinitionsVerifiedAt, DefinitionsValidUntil              time.Time
	AuthorizationFunctionSHA256, DefinitionsReviewSHA256      string
	Supervisor                                                documents.SupervisorConfig
}

func exactID(s string) bool {
	id, e := uuid.Parse(s)
	return e == nil && id.String() == s && id != uuid.Nil
}
func (c configuration) validate(now time.Time) error { return c.validateBindings(now, true) }

func (c configuration) validateBindings(now time.Time, requireFunctionHash bool) error {
	hex := strings.ReplaceAll(c.InstanceID, "-", "")
	if !exactID(c.InstanceID) || !exactID(c.OrganizationID) || !exactID(c.WorkerUserID) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`).MatchString(c.ProfileID) || !strings.HasPrefix(c.InstanceRoot, "/srv/") || filepath.Clean(c.InstanceRoot) != c.InstanceRoot || !strings.HasSuffix(c.InstanceRoot, "/er/"+hex) || c.DatabaseName != "er_"+hex || c.DatabaseRole != "er_processing_"+hex || c.DatabaseDSNFile != c.InstanceRoot+"/config/processing.dsn" || c.Supervisor.JobRoot != c.InstanceRoot+"/jobs" || c.Supervisor.DefinitionsRoot != c.InstanceRoot+"/definitions" || c.Supervisor.MaxConcurrent != 1 || c.Supervisor.DockerPath != "/usr/bin/docker" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.DefinitionsReviewSHA256) || (requireFunctionHash && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.AuthorizationFunctionSHA256)) || c.DefinitionsVerifiedAt.IsZero() || c.DefinitionsVerifiedAt.After(now) || !c.DefinitionsValidUntil.After(now.Add(7*time.Minute)) || c.DefinitionsValidUntil.Sub(c.DefinitionsVerifiedAt) > 24*time.Hour || !c.Supervisor.Worker.SignaturesValidUntil.Equal(c.DefinitionsValidUntil) || len(c.Audiences) == 0 || len(c.Audiences) > 32 {
		return denied
	}
	seen := map[string]bool{}
	for _, a := range c.Audiences {
		if a == "" || a == "*" || len(a) > 200 || seen[a] {
			return denied
		}
		seen[a] = true
	}
	return nil
}

func privateBytes(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, denied
	}
	for p := path; ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil || i.Mode()&os.ModeSymlink != 0 {
			return nil, denied
		}
		if p == "/" {
			break
		}
	}
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > limit {
		return nil, denied
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || st.Nlink != 1 {
		return nil, denied
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, denied
	}
	defer f.Close()
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		return nil, denied
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, denied
	}
	return b, nil
}

func checkPaths(c configuration, root os.FileInfo) error {
	for _, path := range []string{c.InstanceRoot, c.Supervisor.JobRoot, c.Supervisor.DefinitionsRoot} {
		for p := path; p != "/"; p = filepath.Dir(p) {
			i, e := os.Lstat(p)
			if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || i.Mode().Perm()&0022 != 0 {
				return denied
			}
			owner, ok := i.Sys().(*syscall.Stat_t)
			if !ok || owner.Uid != 0 {
				return denied
			}
		}
		var st syscall.Statfs_t
		if syscall.Statfs(path, &st) != nil || uint64(st.Type) != 0x6969 {
			return denied
		}
	}
	current, e := os.Stat(c.InstanceRoot)
	if e != nil || root != nil && !os.SameFile(root, current) {
		return denied
	}
	control := c.InstanceRoot + "/recovery-control"
	if i, e := os.Lstat(control); e == nil {
		if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return denied
		}
		for _, name := range []string{"restore-hold.json", "database-restored.json", "quiesce.json"} {
			if _, e := os.Lstat(control + "/" + name); !errors.Is(e, os.ErrNotExist) {
				return denied
			}
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return denied
	}
	return nil
}

func definitions(ctx context.Context, c configuration) error {
	if c.validate(time.Now().UTC()) != nil {
		return denied
	}
	digest, e := documents.DefinitionsDigest(c.Supervisor.DefinitionsRoot)
	if e != nil || digest != c.Supervisor.DefinitionsHash {
		return denied
	}
	entries, e := os.ReadDir(c.Supervisor.DefinitionsRoot)
	if e != nil || len(entries) != 3 {
		return denied
	}
	expected := map[string]bool{"main.cvd": true, "daily.cvd": true, "bytecode.cvd": true}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return denied
		}
		// ClamAV 1.4 sigtool --info validates the CVD checksum and digital signature.
		// Only signed CVD containers are accepted, never local .ndb/.cld substitutes.
		check, cancel := context.WithTimeout(ctx, 45*time.Second)
		command := exec.CommandContext(check, c.Supervisor.DockerPath, "run", "--rm", "--pull=never", "--network", "none", "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--pids-limit", "32", "--mount", "type=bind,src="+c.Supervisor.DefinitionsRoot+",dst=/defs,readonly", "--entrypoint", "/usr/bin/sigtool", c.Supervisor.Image, "--info", "/defs/"+entry.Name())
		output, e := command.Output()
		cancel()
		if e != nil || len(output) > 16384 || !bytes.Contains(output, []byte("Verification OK.")) {
			return denied
		}
	}
	after, e := documents.DefinitionsDigest(c.Supervisor.DefinitionsRoot)
	if e != nil || after != digest {
		return denied
	}
	return nil
}

func database(ctx context.Context, c configuration) (*sql.DB, error) {
	b, e := privateBytes(c.DatabaseDSNFile, 16384)
	if e != nil {
		return nil, denied
	}
	pc, e := pgx.ParseConfig(strings.TrimSpace(string(b)))
	if e != nil || pc.Database != c.DatabaseName || pc.User != c.DatabaseRole || pc.Password == "" || pc.TLSConfig == nil || pc.TLSConfig.InsecureSkipVerify {
		return nil, denied
	}
	pc.RuntimeParams = map[string]string{"application_name": "er-knowledge-production", "search_path": "public"}
	db := stdlib.OpenDB(*pc)
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(3)
	if e := databaseAuthorization(ctx, db, c); e != nil {
		db.Close()
		return nil, denied
	}
	return db, nil
}

func databaseAuthorization(ctx context.Context, db *sql.DB, c configuration) error {
	var name, user string
	var privileged, member bool
	e := db.QueryRowContext(ctx, `SELECT current_database(),current_user,r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls,EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid) FROM pg_roles r WHERE r.rolname=current_user`).Scan(&name, &user, &privileged, &member)
	if e != nil || name != c.DatabaseName || user != c.DatabaseRole || privileged || member {
		return denied
	}
	var forbidden bool
	e = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND (c.relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) OR (c.relname IN ('messages','contacts','whatsapp_accounts','conversations') AND (has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE') OR has_any_column_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,REFERENCES'))))) OR has_column_privilege(current_user,'public.users','password_hash','SELECT')`).Scan(&forbidden)
	if e != nil || forbidden {
		return denied
	}
	return nil
}

func prepareSupervisor(ctx context.Context, c configuration, verify func(context.Context, configuration) error) (*documents.Supervisor, error) {
	s, e := documents.NewSupervisor(c.Supervisor)
	if e != nil {
		return nil, denied
	}
	if verify(ctx, c) != nil {
		return nil, denied
	}
	return s, nil
}

func serve(ctx context.Context, c configuration, once, checkOnly bool) error {
	if os.Geteuid() != 0 || c.validate(time.Now().UTC()) != nil || checkPaths(c, nil) != nil {
		return denied
	}
	root, e := os.Stat(c.InstanceRoot)
	if e != nil {
		return denied
	}
	// Permanent private root + nonblocking OS lock bound the controller to one lane.
	lock, e := os.OpenFile(c.Supervisor.JobRoot+"/.production-controller.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return denied
	}
	defer lock.Close()
	info, e := lock.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return denied
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || owner.Nlink != 1 {
		return denied
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return denied
	}
	supervisor, e := prepareSupervisor(ctx, c, definitions)
	if e != nil {
		return denied
	}
	db, e := database(ctx, c)
	if e != nil {
		return denied
	}
	defer db.Close()
	authorize := ownerAuthorization(db, c)
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return denied
	}
	e = authorize(ctx, tx, knowledge.ProcessingJob{TenantID: c.OrganizationID, Metadata: knowledge.UploadMetadata{Audiences: c.Audiences}})
	tx.Rollback()
	if e != nil {
		return denied
	}
	if checkOnly {
		return nil
	}
	queue := knowledge.NewAuthorizedProcessingQueue(db, c.OrganizationID, authorize)
	bridge := processing.Bridge{Queue: queue, Processor: supervisor, WorkerID: c.WorkerUserID, ProfileID: c.ProfileID, Image: c.Supervisor.Image, DefinitionsHash: c.Supervisor.DefinitionsHash, Lease: 8 * time.Minute}
	for ctx.Err() == nil {
		if checkPaths(c, root) != nil || definitions(ctx, c) != nil {
			return denied
		}
		e = bridge.Once(ctx)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return denied
		}
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func run(args []string) error {
	flags := flag.NewFlagSet("extensions-processing", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "root-private production JSON")
	once := flags.Bool("once", false, "one bounded claim")
	check := flags.Bool("check", false, "verify current prerequisites without claiming")
	migrationPath := flags.String("authorization-sql", "", "emit new root-private owner-function migration SQL; never apply")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" {
		return denied
	}
	b, e := privateBytes(*path, 65536)
	if e != nil {
		return denied
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var c configuration
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return denied
	}
	if *migrationPath != "" {
		if *once || *check || !filepath.IsAbs(*migrationPath) {
			return denied
		}
		sql, e := authorizationMigration(c)
		if e != nil {
			return denied
		}
		output, e := os.OpenFile(*migrationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return denied
		}
		_, e = output.WriteString(sql)
		if e == nil {
			e = output.Sync()
		}
		closeError := output.Close()
		if e != nil {
			return e
		}
		return closeError
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return serve(ctx, c, *once, *check)
}
func main() {
	if run(os.Args[1:]) != nil {
		os.Stderr.WriteString("production document controller blocked; inspect private owner state\n")
		os.Exit(1)
	}
}
