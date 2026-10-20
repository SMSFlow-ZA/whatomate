// Trusted disposable local restore/worker controller. This is not a web route.
// It verifies the original signed live session against the active identity,
// Redis session and MFA generation before operating on a separate restored DB.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/processing"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
	security "github.com/shridarpatil/whatomate/pkg/extensions/security"
	"os"
	"regexp"
	"time"
)

type provenance struct {
	JobID, TenantID, ActorID, InputHash, SnapshotDatabase string
	Metadata                                              knowledge.UploadMetadata
	CapturedAt                                            time.Time
}
type envelope struct {
	Provenance provenance
	SHA256     string
}

func sum(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func run() error {
	mode := flag.String("mode", "", "capture-provenance, erase-worker, or restore")
	target := flag.String("target", "", "source or dedicated synthetic_restore_<timestamp>")
	role := flag.String("role", "publisher", "publisher or administrator")
	restoreKey := flag.String("restore-key", "", "unique logical offline restore identity")
	flag.Parse()
	if os.Getenv("SYNTHETIC_LOCAL_ONLY") != "true" || (*role != "publisher" && *role != "administrator") {
		return errors.New("explicit trusted synthetic controller required")
	}
	cfg, e := config.Load("/run/private/host.toml")
	if e != nil {
		return e
	}
	if cfg.App.Environment != "development" || !cfg.Extensions.SyntheticDevelopment || cfg.Database.Name != "whatomate_synthetic_local" {
		return errors.New("dedicated synthetic source required")
	}
	source, e := database.NewPostgres(&cfg.Database, false)
	if e != nil {
		return e
	}
	sourceRaw, e := source.DB()
	if e != nil {
		return e
	}
	defer sourceRaw.Close()
	redis, e := database.NewRedis(&cfg.Redis)
	if e != nil {
		return e
	}
	defer redis.Close()
	key, e := base64.StdEncoding.DecodeString(cfg.Extensions.SecurityKey)
	if e != nil {
		return e
	}
	mfa, e := security.New(sourceRaw, key, security.Config{Drift: 1, EnrollmentTTL: 10 * time.Minute, MaxBreakGlass: time.Hour, AllowedBreakGlassPermissions: []string{"cases:read", "cases:work.assigned"}})
	if e != nil {
		return e
	}
	app := handlers.App{DB: source, Redis: redis, Config: cfg, ExtensionSecurity: mfa, ExtensionMFAFreshness: time.Duration(cfg.Extensions.MFAFreshnessSeconds) * time.Second}
	b, e := os.ReadFile("/run/private/root-api-state.json")
	if e != nil {
		return e
	}
	var sessions map[string]struct {
		Cookies []struct{ Name, Value string } `json:"cookies"`
	}
	if json.Unmarshal(b, &sessions) != nil {
		return errors.New("invalid private signed session store")
	}
	encoded := ""
	for _, cookie := range sessions[*role].Cookies {
		if cookie.Name == "whm_access" {
			if encoded != "" {
				return errors.New("ambiguous signed session")
			}
			encoded = cookie.Value
		}
	}
	claims, e := middleware.ParseToken(cfg.JWT.Secret, encoded, "access")
	if e != nil {
		return errors.New("original signed session invalid or expired")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	refresh := func(ctx context.Context) (api.Actor, error) {
		currentClaims, e := middleware.ParseToken(cfg.JWT.Secret, encoded, "access")
		if e != nil {
			return api.Actor{}, e
		}
		if currentClaims.UserID != claims.UserID || currentClaims.SessionID != claims.SessionID {
			return api.Actor{}, errors.New("original session changed")
		}
		actor, e := app.ExtensionActorFromClaims(ctx, currentClaims)
		if e != nil {
			return api.Actor{}, e
		}
		return (api.AccessStore{DB: sourceRaw}).Load(ctx, api.Actor{OrganizationID: actor.OrganizationID, UserID: actor.UserID, SessionID: actor.SessionID, MFA: actor.MFA, Permissions: actor.Permissions})
	}
	actor, e := refresh(ctx)
	if e != nil {
		return e
	}
	b, e = os.ReadFile("/run/private/v02-state.json")
	if e != nil {
		return e
	}
	var state struct {
		JobID   string `json:"withdrawal_job_id"`
		Subject string `json:"governance_subject"`
		Request string `json:"governance_request"`
	}
	if json.Unmarshal(b, &state) != nil || !regexp.MustCompile(`^[a-f0-9]{48}$`).MatchString(state.JobID) || state.Subject != "V02-ISOLATED-WITHDRAWAL" || state.Request != "v02-doc-delete-20261020" {
		return errors.New("isolated accepted test identity required")
	}
	dbCfg := cfg.Database
	if *target == "source" {
		if *mode != "erase-worker" {
			return errors.New("source target only supports exact worker completion")
		}
	} else {
		if !regexp.MustCompile(`^synthetic_restore_[0-9]{14}$`).MatchString(*target) {
			return errors.New("dedicated offline restore DB required")
		}
		dbCfg.Name = *target
	}
	targetDB, e := database.NewPostgres(&dbCfg, false)
	if e != nil {
		return e
	}
	raw, e := targetDB.DB()
	if e != nil {
		return e
	}
	defer raw.Close()
	var count int
	if e = raw.QueryRowContext(ctx, "SELECT count(*) FROM organizations WHERE id::text=$1 AND slug='synthetic-er-local'", actor.OrganizationID).Scan(&count); e != nil || count != 1 {
		return errors.New("source/restore organisation marker mismatch")
	}
	scope := governance.Scope{OrganizationID: actor.OrganizationID, UnitID: "employees"}
	physical := governance.PhysicalService{DB: raw, Adapters: []governance.PhysicalAdapter{knowledge.PrivacyAdapter{}}}
	service := governance.Service{Repository: &governance.SQLRepository{DB: raw}}
	readProof := func() (provenance, error) {
		var wrapped envelope
		b, e := os.ReadFile("/run/output/v02-worker-provenance.json")
		if e != nil {
			return provenance{}, e
		}
		if json.Unmarshal(b, &wrapped) != nil || wrapped.SHA256 != sum(wrapped.Provenance) {
			return provenance{}, errors.New("provenance identity mismatch")
		}
		p := wrapped.Provenance
		if p.JobID != state.JobID || p.TenantID != actor.OrganizationID || p.Metadata.DocumentID != state.Subject || !regexp.MustCompile(`^synthetic_restore_[0-9]{14}$`).MatchString(p.SnapshotDatabase) {
			return p, errors.New("untrusted original provenance")
		}
		return p, nil
	}
	switch *mode {
	case "capture-provenance":
		if !actor.Allows(string(governance.JournalRead), scope.UnitID) {
			return governance.ErrDenied
		}
		var p provenance
		var metadata []byte
		var input []byte
		e = raw.QueryRowContext(ctx, "SELECT id,tenant,actor,input_hash,metadata,input FROM addon_knowledge_jobs WHERE id=$1 AND tenant=$2", state.JobID, actor.OrganizationID).Scan(&p.JobID, &p.TenantID, &p.ActorID, &p.InputHash, &metadata, &input)
		if e != nil {
			return e
		}
		if json.Unmarshal(metadata, &p.Metadata) != nil || p.Metadata.DocumentID != state.Subject || fmt.Sprintf("%x", sha256.Sum256(input)) != p.InputHash {
			return errors.New("original snapshot provenance mismatch")
		}
		for _, aud := range p.Metadata.Audiences {
			if !actor.AudienceAllowed("knowledge:read", aud) || !actor.AudienceAllowed("knowledge:retire", aud) {
				return governance.ErrDenied
			}
		}
		p.SnapshotDatabase = *target
		p.CapturedAt = time.Now().UTC()
		f, e := os.OpenFile("/run/output/v02-worker-provenance.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
		if e != nil {
			return e
		}
		defer f.Close()
		if e = json.NewEncoder(f).Encode(envelope{Provenance: p, SHA256: sum(p)}); e != nil {
			return e
		}
		return f.Sync()
	case "restore":
		if *restoreKey == "" || *target == "source" {
			return errors.New("stable restore identity required")
		}
		if _, e = readProof(); e != nil {
			return e
		}
		actor, e = refresh(ctx)
		if e != nil {
			return e
		}
		entries, e := physical.ReconcileSubjectRestore(ctx, actor, scope, state.Subject, state.Request, *restoreKey)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Mode    string
			Entries []governance.JournalEntry
		}{"offline-source-reapplied", entries})
	case "erase-worker":
		proof, e := readProof()
		if e != nil {
			return e
		}
		var c processing.ErasureConfig
		b, e := os.ReadFile("/run/config/erasure.json")
		if e != nil {
			return e
		}
		if json.Unmarshal(b, &c) != nil {
			return errors.New("invalid trusted erasure config")
		}
		entries, e := service.Journal(ctx, governance.PrincipalForActor(actor), scope, state.Request)
		if e != nil {
			return e
		}
		recordID := "knowledge:" + state.JobID + ":worker-files"
		var version uint64
		for _, entry := range entries {
			if entry.RecordID == recordID {
				version = entry.Version
			}
		}
		if version == 0 {
			return errors.New("worker journal entry missing")
		}
		result, e := physical.CompleteWorkerErasure(ctx, actor, scope, state.Request, recordID, version, func(ctx context.Context, original api.Actor, r governance.Record) (governance.WorkerErasureReceipt, error) {
			current, e := refresh(ctx)
			if e != nil {
				return governance.WorkerErasureReceipt{}, e
			}
			if current.UserID != original.UserID || current.SessionID != original.SessionID || r.Reference != proof.JobID || r.SubjectID != proof.Metadata.DocumentID || r.Scope != scope {
				return governance.WorkerErasureReceipt{}, governance.ErrDenied
			}
			for _, aud := range proof.Metadata.Audiences {
				if !current.AudienceAllowed("knowledge:read", aud) || !current.AudienceAllowed("knowledge:retire", aud) {
					return governance.WorkerErasureReceipt{}, governance.ErrDenied
				}
			}
			request := processing.ErasureRequest{JobID: proof.JobID, TenantID: proof.TenantID, ActorID: proof.ActorID, InputHash: proof.InputHash}
			receipt, e := processing.EraseJob(ctx, c, request)
			if e != nil {
				return governance.WorkerErasureReceipt{}, e
			}
			if e = processing.VerifyErasure(ctx, c, request, receipt); e != nil {
				return governance.WorkerErasureReceipt{}, e
			}
			return governance.WorkerErasureReceipt{JobID: receipt.JobID, TenantID: receipt.TenantID, ActorID: receipt.ActorID, InputHash: receipt.InputHash, ManifestHash: receipt.ManifestHash, ReceiptHash: receipt.ReceiptHash, DeletedAt: receipt.DeletedAt}, nil
		})
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	default:
		return errors.New("invalid trusted controller mode")
	}
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "synthetic owner reconciliation failed:", e)
		os.Exit(1)
	}
}
