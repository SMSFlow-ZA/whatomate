package handlers

import (
	"context"
	"database/sql"
	"errors"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/notifications"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestNotificationHostResolvesCurrentRoleAndAllAliases(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ns := "notify_host_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = base.Exec(`CREATE SCHEMA ` + ns); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", ns)
	u.RawQuery = q.Encode()
	gdb, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); base.Exec(`DROP SCHEMA ` + ns + ` CASCADE`); base.Close() })
	_, err = db.Exec(`CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid NOT NULL,role_id uuid,is_active boolean NOT NULL,deleted_at timestamptz);
CREATE TABLE custom_roles(id uuid PRIMARY KEY,organization_id uuid NOT NULL,deleted_at timestamptz);
CREATE TABLE permissions(id uuid PRIMARY KEY,resource text NOT NULL,action text NOT NULL,deleted_at timestamptz);
CREATE TABLE role_permissions(custom_role_id uuid,permission_id uuid,PRIMARY KEY(custom_role_id,permission_id))`)
	if err != nil {
		t.Fatal(err)
	}
	access := &addonapi.AccessStore{DB: db}
	if err = access.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	org, user, role, permission := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := db.Exec(query, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO users VALUES($1,$2,$3,true,NULL)`, user, org, role)
	exec(`INSERT INTO custom_roles VALUES($1,$2,NULL)`, role, org)
	exec(`INSERT INTO permissions VALUES($1,'cases','work.assigned',NULL)`, permission)
	exec(`INSERT INTO role_permissions VALUES($1,$2)`, role, permission)
	if err = access.Replace(ctx, org, user, []addonapi.Grant{{Permission: string(cases.WorkAssigned), UnitID: "case-zone"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"employee-old", "employee-current"} {
		if err = access.LinkIdentity(ctx, org, user, id); err != nil {
			t.Fatal(err)
		}
	}
	a := App{DB: gdb, ExtensionAccess: access}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := a.resolveHandlerNotificationIdentity(cancelled, org, user); e == nil || errors.Is(e, notifications.ErrDenied) || errors.Is(e, addonapi.ErrDenied) {
		t.Fatal("temporary database cancellation must not permanently consume a notification", e)
	}
	got, err := a.resolveHandlerNotificationIdentity(ctx, org, user)
	if err != nil || !got.Active || len(got.EmployeeIDs) != 2 || len(got.Principal.Aliases) != 2 || len(got.Principal.Grants) != 1 {
		t.Fatalf("current member/aliases not preserved: %#v %v", got, err)
	}
	exec(`UPDATE users SET is_active=false WHERE id=$1`, user)
	if _, err = a.resolveHandlerNotificationIdentity(ctx, org, user); err == nil {
		t.Fatal("inactive handler accepted")
	}
	exec(`UPDATE users SET is_active=true WHERE id=$1`, user)
	exec(`DELETE FROM role_permissions WHERE custom_role_id=$1`, role)
	if _, err = a.resolveHandlerNotificationIdentity(ctx, org, user); err == nil {
		t.Fatal("revoked role permission accepted")
	}
	exec(`INSERT INTO role_permissions VALUES($1,$2)`, role, permission)
	if _, err = a.resolveHandlerNotificationIdentity(ctx, uuid.NewString(), user); err == nil {
		t.Fatal("cross organization membership accepted")
	}
	exec(`INSERT INTO addon_api_identity_links(organization_id,user_id,employee_id) VALUES($1,$2,'other-org-alias')`, uuid.NewString(), user)
	if _, err = a.resolveHandlerNotificationIdentity(ctx, org, user); err == nil {
		t.Fatal("cross organization historical alias ignored")
	}
}
