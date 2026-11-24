package handlers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type inboxIntegrationSafeStorageError struct{ code string }

// Sensitive writes stay private even when the application's SQL debug logger
// is enabled. Structured error categories remain available to the caller.
func privateInboxIntegrationDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{Logger: logger.Discard})
}

func (e inboxIntegrationSafeStorageError) Error() string {
	return "integration persistence error [" + e.code + "]"
}

// PostgreSQL detail/binding errors can contain an entire failed field row.
// Retain a safe SQLSTATE for diagnosis, never provider data or wrapped text.
func inboxIntegrationPersistenceError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{gorm.ErrRecordNotFound, errInboxConflict, errInboxTransition, errInboxDisabled, errRequiredInboxFields, errInboxAgentAtCapacity, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	var safe inboxIntegrationSafeStorageError
	if errors.As(err, &safe) {
		return safe
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		code := pgError.Code
		valid := len(code) == 5
		for _, r := range code {
			valid = valid && (r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
		}
		if valid {
			return inboxIntegrationSafeStorageError{code}
		}
	}
	return inboxIntegrationSafeStorageError{"unknown"}
}
