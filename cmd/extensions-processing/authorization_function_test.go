//go:build linux

package main

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"testing"
)

func TestStaticAuthorizationBindingDeniesMissingForeignAndWorkerRegrant(t *testing.T) {
	db, worker, c := ownerFixture(t)
	ctx := context.Background()
	allowed := func() bool {
		var value bool
		if e := worker.QueryRow("SELECT public.er_processing_authorize_v1($1,$2,$3,$4::text[])", c.OrganizationID, c.WorkerUserID, c.ProfileID, c.Audiences).Scan(&value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	if !allowed() {
		t.Fatal("reviewed binding refused")
	}
	if _, e := worker.Exec("UPDATE public.er_processing_authorization_bindings SET profile_id='forged'"); e == nil {
		t.Fatal("worker changed binding")
	}
	if _, e := db.Exec("UPDATE public.er_processing_authorization_bindings SET worker_role='not_this_session'"); e != nil {
		t.Fatal(e)
	}
	if allowed() {
		t.Fatal("foreign session binding accepted")
	}
	if _, e := db.Exec("UPDATE public.er_processing_authorization_bindings SET worker_role=$1,organization_id='00000000-0000-0000-0000-000000000000'", c.DatabaseRole); e != nil {
		t.Fatal(e)
	}
	if allowed() {
		t.Fatal("foreign organization binding accepted")
	}
	if _, e := db.Exec("DELETE FROM public.er_processing_authorization_bindings"); e != nil {
		t.Fatal(e)
	}
	if allowed() {
		t.Fatal("empty baseline binding authorized")
	}
	if _, e := db.Exec("INSERT INTO public.er_processing_authorization_bindings(database_name,worker_role,organization_id,worker_user_id,profile_id,audiences) VALUES($1,$2,$3,$4,$5,$6::text[])", c.DatabaseName, c.DatabaseRole, c.OrganizationID, c.WorkerUserID, c.ProfileID, c.Audiences); e != nil {
		t.Fatal(e)
	}
	if !allowed() {
		t.Fatal("restored reviewed binding refused")
	}
	if _, e := db.Exec("GRANT UPDATE(profile_id) ON public.er_processing_authorization_bindings TO " + c.DatabaseRole); e != nil {
		t.Fatal(e)
	}
	tx, e := worker.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	j := knowledge.ProcessingJob{TenantID: c.OrganizationID, Metadata: knowledge.UploadMetadata{Audiences: c.Audiences}}
	if e = ownerAuthorization(worker, c)(ctx, tx, j); e == nil {
		t.Fatal("binding column regrant accepted")
	}
}
