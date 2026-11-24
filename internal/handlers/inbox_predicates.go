package handlers

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxpredicate"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

func inboxPredicateError(err error) error {
	if errors.Is(err, inboxpredicate.ErrInvalid) {
		return errInboxTransition
	}
	return err
}

func inboxPredicateDefinitions(db *gorm.DB, org uuid.UUID, predicates []models.InboxPredicate, allowContact bool) (map[uuid.UUID]models.InboxFieldDefinition, error) {
	definitions, err := inboxpredicate.Definitions(db, org, predicates, allowContact)
	return definitions, inboxPredicateError(err)
}

func inboxPredicateSQL(db *gorm.DB, org uuid.UUID, p models.InboxPredicate, allowContact bool) (string, []any, error) {
	sql, args, err := inboxpredicate.SQL(db, org, p, allowContact)
	return sql, args, inboxPredicateError(err)
}

func compiledInboxPredicateSQL(p models.InboxPredicate, definitions map[uuid.UUID]models.InboxFieldDefinition) (string, []any, error) {
	sql, args, err := inboxpredicate.Compile(p, definitions)
	return sql, args, inboxPredicateError(err)
}

func applyInboxPredicates(db, q *gorm.DB, org uuid.UUID, f models.InboxQueueFilter, allowContact bool) (*gorm.DB, error) {
	result, err := inboxpredicate.Apply(db, q, org, f, allowContact)
	return result, inboxPredicateError(err)
}

func validateInboxPredicates(db *gorm.DB, org uuid.UUID, f models.InboxQueueFilter, allowContact bool) error {
	_, err := applyInboxPredicates(db, db.Model(&models.InboxCase{}), org, f, allowContact)
	if err != nil {
		return fmt.Errorf("%w: invalid or unavailable condition", errInboxTransition)
	}
	return nil
}
