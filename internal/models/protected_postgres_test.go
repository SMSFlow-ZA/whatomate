package models

import (
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"testing"
)

func TestProtectionPostgresImmutableAndHidden(t *testing.T) {
	dsn := os.Getenv("PROTECTION_TEST_DSN")
	if dsn == "" {
		t.Skip("set PROTECTION_TEST_DSN to disposable PostgreSQL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	schema := "protection_" + uuid.New().String()[:8]
	if err = db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err = db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"contacts", "messages"} {
		err = db.Exec("CREATE TABLE " + table + ` (id uuid PRIMARY KEY, organization_id uuid NOT NULL, contact_id uuid, content text, deleted_at timestamptz, metadata jsonb DEFAULT '{}', protection_organization_id text NOT NULL DEFAULT '', protection_module text NOT NULL DEFAULT '', protection_resource text NOT NULL DEFAULT '', protection_classification text NOT NULL DEFAULT '')`).Error
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = InstallProtectionGuards(db); err != nil {
		t.Fatal(err)
	}
	org, contact, message := uuid.New(), uuid.New(), uuid.New()
	err = db.Exec(`INSERT INTO contacts(id,organization_id) VALUES(?,?)`, contact, org).Error
	if err != nil {
		t.Fatal(err)
	}
	err = db.Exec(`INSERT INTO messages(id,organization_id,contact_id,content,protection_organization_id,protection_module,protection_resource,protection_classification) VALUES(?,?,?,'SECRET-MARKER',?,'casework','case-1','sensitive')`, message, org, contact, org.String()).Error
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"UPDATE messages SET protection_module = '' WHERE id = ?",
		"UPDATE messages SET protection_resource = 'other' WHERE id = ?",
		"UPDATE messages SET organization_id = '00000000-0000-0000-0000-000000000000' WHERE id = ?",
	} {
		if err = db.Exec(stmt, message).Error; err == nil {
			t.Fatal("protection edit accepted")
		}
	}
	if err = db.Exec(`UPDATE messages SET metadata = '{}' WHERE id = ?`, message).Error; err != nil {
		t.Fatal(err)
	}
	if err = RegisterProtectionReadGuards(db); err != nil {
		t.Fatal(err)
	}
	var messages []Message
	if err = db.Where("content LIKE ?", "%SECRET-MARKER%").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatal("marker exposed by search")
	}
	var contacts []Contact
	if err = db.Find(&contacts).Error; err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 0 {
		t.Fatal("protected child preview exposed")
	}
	var count int64
	if err = db.Model(&Message{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("protected count exposed")
	}
}
