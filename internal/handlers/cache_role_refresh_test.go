package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/test/testutil"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func permissionRefreshFixture(t *testing.T) (*App, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL required")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "permission_refresh_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	gdb, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(options)
	if err = rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	user, home, foreign, role := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		ctx := context.Background()
		rdb.Del(ctx, userPermissionsCachePrefix+user.String(), userPermissionsCachePrefix+user.String()+":"+home.String(), userPermissionsCachePrefix+user.String()+":"+foreign.String())
		rdb.Close()
		db.Close()
		base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		base.Close()
	})
	_, err = db.Exec(`CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid,role_id uuid,is_active boolean,is_super_admin boolean,deleted_at timestamptz);
 CREATE TABLE user_organizations(id uuid PRIMARY KEY,user_id uuid,organization_id uuid,role_id uuid,deleted_at timestamptz);
 CREATE TABLE custom_roles(id uuid PRIMARY KEY,organization_id uuid,name text,is_system boolean,deleted_at timestamptz);
 CREATE TABLE permissions(id uuid PRIMARY KEY,resource text,action text,deleted_at timestamptz);
 CREATE TABLE role_permissions(custom_role_id uuid,permission_id uuid)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users VALUES($1,$2,$3,true,false,NULL)`, []any{user, home, role}},
		{`INSERT INTO user_organizations VALUES($1,$2,$3,$4,NULL)`, []any{uuid.New(), user, home, role}},
		{`INSERT INTO custom_roles VALUES($1,$2,'old-role',false,NULL)`, []any{role, home}},
		{`INSERT INTO permissions VALUES($1,'contacts','delete',NULL)`, []any{uuid.New()}},
		{`INSERT INTO role_permissions SELECT $1,id FROM permissions`, []any{role}},
	} {
		if _, err = db.Exec(statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return &App{DB: gdb, Redis: rdb, Log: testutil.NopLogger()}, user, home, foreign, role
}

func TestPermissionsCacheDirectRoleReplacementRevokesHomeAndOrgScopes(t *testing.T) {
	for _, orgScoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("org_scope_%t", orgScoped), func(t *testing.T) {
			a, user, home, _, _ := permissionRefreshFixture(t)
			scope := []uuid.UUID{}
			if orgScoped {
				scope = append(scope, home)
			}
			if !a.HasPermission(user, "contacts", "delete", scope...) {
				t.Fatal("could not prime actual granted permission")
			}
			key := userPermissionsCachePrefix + user.String()
			if orgScoped {
				key += ":" + home.String()
			}
			if _, err := a.Redis.Get(context.Background(), key).Result(); err != nil {
				t.Fatal("permission was not cached")
			}
			replacement := uuid.New()
			db, _ := a.DB.DB()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`INSERT INTO custom_roles VALUES($1,$2,'empty-replacement',false,NULL)`, replacement, home); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`UPDATE users SET role_id=$1 WHERE id=$2`, replacement, user); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`UPDATE user_organizations SET role_id=$1 WHERE user_id=$2 AND organization_id=$3`, replacement, user, home); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if a.HasPermission(user, "contacts", "delete", scope...) {
				t.Fatal("direct atomic role replacement retained cached generic grant")
			}
		})
	}
}

func TestPermissionsCacheCurrentRoleAndMembershipFailClosed(t *testing.T) {
	for _, change := range []string{"deleted-role", "foreign-role", "home-no-role", "foreign-no-role", "foreign-removed"} {
		t.Run(change, func(t *testing.T) {
			a, user, home, foreign, role := permissionRefreshFixture(t)
			org := home
			db, _ := a.DB.DB()
			if change == "foreign-no-role" || change == "foreign-removed" {
				org = foreign
				foreignRole := uuid.New()
				if _, err := db.Exec(`INSERT INTO custom_roles VALUES($1,$2,'foreign-membership-role',false,NULL)`, foreignRole, foreign); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO role_permissions SELECT $1,id FROM permissions`, foreignRole); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO user_organizations VALUES($1,$2,$3,$4,NULL)`, uuid.New(), user, foreign, foreignRole); err != nil {
					t.Fatal(err)
				}
			}
			if !a.HasPermission(user, "contacts", "delete", org) {
				t.Fatal("could not prime scoped grant")
			}
			var err error
			switch change {
			case "deleted-role":
				_, err = db.Exec(`UPDATE custom_roles SET deleted_at=clock_timestamp() WHERE id=$1`, role)
			case "foreign-role":
				_, err = db.Exec(`UPDATE custom_roles SET organization_id=$1 WHERE id=$2`, foreign, role)
			case "home-no-role", "foreign-no-role":
				_, err = db.Exec(`UPDATE user_organizations SET role_id=NULL WHERE user_id=$1 AND organization_id=$2`, user, org)
			case "foreign-removed":
				_, err = db.Exec(`DELETE FROM user_organizations WHERE user_id=$1 AND organization_id=$2`, user, org)
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.HasPermission(user, "contacts", "delete", org) {
				t.Fatal("invalid current membership/role trusted cached privilege")
			}
		})
	}
}

func TestPermissionsCacheLegacyHomeFallbackAndCurrentSuperadmin(t *testing.T) {
	a, user, home, foreign, role := permissionRefreshFixture(t)
	db, _ := a.DB.DB()
	if _, err := db.Exec(`DELETE FROM user_organizations WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if !a.HasPermission(user, "contacts", "delete", home) {
		t.Fatal("legacy primary-org role fallback removed")
	}
	if _, err := db.Exec(`UPDATE users SET is_super_admin=true,role_id=NULL WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if !a.HasPermission(user, "contacts", "delete", foreign) {
		t.Fatal("current superadmin semantics removed")
	}
	if _, err := db.Exec(`UPDATE users SET is_super_admin=false,role_id=NULL WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(UserPermissions{RoleID: role, IsSuperAdmin: true, Permissions: []string{"contacts:delete"}})
	if err := a.Redis.Set(context.Background(), userPermissionsCachePrefix+user.String(), raw, userPermissionsCacheTTL).Err(); err != nil {
		t.Fatal(err)
	}
	if a.HasPermission(user, "contacts", "delete") {
		t.Fatal("demoted current account retained cached role grants")
	}
}
