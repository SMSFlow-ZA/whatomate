package models

import (
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestProtectionRejectsPartialAndCrossOrganization(t *testing.T) {
	org := uuid.New()
	for _, p := range []ProtectionFields{
		{ProtectionModule: "casework"},
		{ProtectionOrganizationID: uuid.New().String(), ProtectionModule: "casework", ProtectionResource: "case-1", ProtectionClassification: "sensitive"},
	} {
		if _, err := p.Value(org); err == nil {
			t.Fatal("invalid protection accepted")
		}
	}
	if (ProtectionFields{}).Protected() {
		t.Fatal("ordinary record became protected")
	}
}

func TestProtectionReadGuardsExcludeBeforeCountAndSearch(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=invalid", PreferSimpleProtocol: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterProtectionReadGuards(db); err != nil {
		t.Fatal(err)
	}
	var rows []Message
	query := db.Where("content LIKE ?", "%SECRET-MARKER%").Find(&rows)
	sql := query.Statement.SQL.String()
	if !strings.Contains(sql, "protection_module") || !strings.Contains(sql, "contacts") {
		t.Fatalf("unguarded search: %s", sql)
	}
	var count int64
	query = db.Model(&Contact{}).Count(&count)
	if !strings.Contains(query.Statement.SQL.String(), "protection_module") {
		t.Fatal("unguarded contact count")
	}
}
