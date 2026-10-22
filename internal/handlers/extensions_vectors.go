package handlers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

const extensionVectorIndexer = "knowledge-vector-indexer-v1"

var errExtensionVectors = errors.New("extension vector composition denied")

func (a *App) extensionVectorPin() (knowledge.VectorPin, error) {
	if a == nil || a.Config == nil {
		return knowledge.VectorPin{}, errExtensionVectors
	}
	c := a.Config.Extensions
	identity := strings.TrimSpace(c.KnowledgeVectorIdentity)
	modules := map[string]bool{}
	for _, id := range extensionIDs(c.Modules) {
		modules[id] = true
	}
	if !modules["knowledge"] || !modules["ai"] || identity == "" || identity != c.KnowledgeVectorIdentity || len(identity) > 256 || identity == c.AIEmbeddingModel || strings.HasPrefix(strings.ToUpper(identity), "REQUIRED") || strings.HasPrefix(strings.ToUpper(identity), "PENDING") || c.AIEmbeddingDimensions < 1 || c.AIEmbeddingDimensions > 2000 {
		return knowledge.VectorPin{}, errExtensionVectors
	}
	// Identity is operator-reviewed deployment-generation evidence, not proof that
	// this helper verified the external provider. Trusted release composition must
	// bind both indexing and query providers to the same actual model generation.
	return knowledge.VectorPin{Model: identity, Dimensions: c.AIEmbeddingDimensions}, nil
}

func (a *App) extensionVectorPool(db *sql.DB) error {
	if a == nil || a.DB == nil || db == nil {
		return errExtensionVectors
	}
	actual, err := a.DB.DB()
	if err != nil {
		return err
	}
	if actual != db {
		return errExtensionVectors
	}
	return nil
}

func (a *App) extensionVectorOrganizations(ctx context.Context) ([]string, error) {
	if a == nil || a.DB == nil {
		return nil, errExtensionVectors
	}
	var organizations []models.Organization
	if err := a.DB.WithContext(ctx).Select("id").Order("id").Find(&organizations).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(organizations))
	for _, org := range organizations {
		if org.ID == uuid.Nil {
			return nil, errExtensionVectors
		}
		ids = append(ids, org.ID.String())
	}
	return ids, nil
}

// initializeExtensionVectors is a trusted startup migration port. Root invokes it
// after compiled knowledge/provider initialization and before marking protection
// enabled. Requests and background ticks never invoke this method.
func (a *App) initializeExtensionVectors(ctx context.Context, db *sql.DB) error {
	if a == nil || a.Config == nil {
		return errExtensionVectors
	}
	if !a.Config.Extensions.KnowledgeVectorEnabled {
		return nil
	}
	if err := a.extensionVectorPool(db); err != nil {
		return err
	}
	pin, err := a.extensionVectorPin()
	if err != nil {
		return err
	}
	if _, err = newExtensionAIProvider(extensionAIProviderConfigFromHost(a.Config.Extensions)); err != nil {
		return err
	}
	ids, err := a.extensionVectorOrganizations(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errExtensionVectors
	}
	for _, tenant := range ids {
		repository, e := knowledge.NewVectorRepository(db, tenant, pin)
		if e != nil {
			return e
		}
		if e = a.extensionSchemaMigration(ctx, db, func() error {
			if err := repository.Migrate(ctx); err != nil {
				return err
			}
			return repository.RegisterIndexer(ctx, extensionVectorIndexer)
		}); e != nil {
			return e
		}
		if e = repository.Verify(ctx); e != nil {
			return e
		}
	}
	return nil
}

// extensionVectors verifies the authenticated organization's current host tuple
// and immutable profile without migration or worker-grant registration.
func (a *App) extensionVectors(ctx context.Context, db *sql.DB, tenant string) (*knowledge.VectorRepository, error) {
	if a == nil || a.Config == nil {
		return nil, errExtensionVectors
	}
	if !a.Config.Extensions.KnowledgeVectorEnabled {
		return nil, nil
	}
	if err := a.extensionVectorPool(db); err != nil {
		return nil, err
	}
	pin, err := a.extensionVectorPin()
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(tenant)
	if err != nil || id == uuid.Nil || id.String() != tenant {
		return nil, errExtensionVectors
	}
	var count int64
	if err = a.DB.WithContext(ctx).Model(&models.Organization{}).Where("id=?", id).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, errExtensionVectors
	}
	repository, err := knowledge.NewVectorRepository(db, tenant, pin)
	if err != nil {
		return nil, err
	}
	if err = repository.Verify(ctx); err != nil {
		return nil, err
	}
	return repository, nil
}

// extensionVectorTick indexes at most one page per existing organization. It is
// scheduled by its own bounded root-owned worker lane; it is not a request route.
// Errors for one organization do not skip the remaining organizations.
func (a *App) extensionVectorTick(ctx context.Context) error {
	if a == nil || a.Config == nil {
		return errExtensionVectors
	}
	if !a.Config.Extensions.KnowledgeVectorEnabled {
		return nil
	}
	if a.DB == nil {
		return errExtensionVectors
	}
	db, err := a.DB.DB()
	if err != nil {
		return err
	}
	provider, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(a.Config.Extensions))
	if err != nil {
		return err
	}
	ids, err := a.extensionVectorOrganizations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for index, tenant := range ids {
		if err = ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		budget := 60 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			share := time.Until(deadline) / time.Duration(len(ids)-index)
			if share < budget {
				budget = share
			}
		}
		tick, cancel := context.WithTimeout(ctx, budget)
		repository, e := a.extensionVectors(tick, db, tenant)
		if e == nil && repository != nil {
			_, e = repository.IndexNext(tick, extensionVectorIndexer, provider)
		}
		cancel()
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
