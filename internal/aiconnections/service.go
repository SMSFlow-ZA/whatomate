// Package aiconnections binds approved inference endpoints to chatbot settings.
package aiconnections

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/aiproviders"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const probePrompt = "Reply with the single word READY. This is a synthetic connection test."

var (
	ErrOff      = errors.New("ai_connections_disabled")
	ErrMissing  = errors.New("ai_connection_not_found")
	ErrAssigned = errors.New("ai_connection_assigned")
	ErrNotReady = errors.New("ai_connection_not_ready")
	ErrChanged  = errors.New("ai_connection_changed")
	ErrBusy     = errors.New("ai_connection_busy")
)

type endpoint struct {
	target   aiproviders.Target
	provider aiproviders.Provider
}

type Service struct {
	db        *gorm.DB
	key       string
	enabled   bool
	endpoints map[string]endpoint
	mu        sync.Mutex
	busy      map[uuid.UUID]bool
}

func New(db *gorm.DB, key string, enabled bool, policy string, factory func(aiproviders.Target) aiproviders.Provider) (*Service, error) {
	s := &Service{db: db, key: key, endpoints: map[string]endpoint{}, busy: map[uuid.UUID]bool{}}
	if !enabled {
		return s, nil
	}
	if len(key) < 32 || len(policy) > 64*1024 {
		return s, ErrOff
	}
	var specs []aiproviders.Endpoint
	decoder := json.NewDecoder(strings.NewReader(policy))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&specs) != nil || decoder.Decode(new(any)) != io.EOF || len(specs) == 0 || len(specs) > 32 {
		return s, aiproviders.ErrEndpoint
	}
	for _, spec := range specs {
		t, err := aiproviders.ParseEndpoint(spec)
		if err != nil {
			return s, aiproviders.ErrEndpoint
		}
		if _, exists := s.endpoints[t.ID()]; exists {
			return s, aiproviders.ErrEndpoint
		}
		provider := aiproviders.Provider(aiproviders.NewChat(t))
		if factory != nil {
			provider = factory(t)
		}
		s.endpoints[t.ID()] = endpoint{target: t, provider: provider}
	}
	s.enabled = true
	return s, nil
}

type EndpointOption struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Host      string `json:"host"`
	LocalOnly bool   `json:"local_only"`
}

func (s *Service) Enabled() bool { return s != nil && s.enabled }
func (s *Service) Options() []EndpointOption {
	result := []EndpointOption{}
	for _, entry := range s.endpoints {
		t := entry.target
		result = append(result, EndpointOption{ID: t.ID(), Label: t.Label(), Kind: t.Kind(), Host: t.Host(), LocalOnly: t.LocalOnly()})
	}
	return result
}

func (s *Service) Ready(c models.ChatbotAIConnection) bool {
	ep, ok := s.endpoints[c.EndpointID]
	return s.enabled && c.Enabled && ok && ep.target.AllowsModel(c.Model) && c.TestedAt != nil && c.CredentialScope == ep.target.CredentialScope() && c.TestedPolicy == ep.target.Fingerprint()
}

func (s *Service) List(ctx context.Context, org uuid.UUID) ([]models.ChatbotAIConnection, []models.ChatbotAIBinding, error) {
	db := s.db.WithContext(ctx)
	connections := []models.ChatbotAIConnection{}
	bindings := []models.ChatbotAIBinding{}
	if err := db.Where("organization_id = ?", org).Order("name, id").Find(&connections).Error; err != nil {
		return nil, nil, err
	}
	err := db.Where("organization_id = ?", org).Order("account_id").Find(&bindings).Error
	return connections, bindings, err
}

func (s *Service) Create(ctx context.Context, org uuid.UUID, name, id, model, credential string) (models.ChatbotAIConnection, error) {
	name = strings.TrimSpace(name)
	if !s.enabled {
		return models.ChatbotAIConnection{}, ErrOff
	}
	if !validName(name) || !s.validEndpoint(id, model) || !aiproviders.ValidCredential(credential) {
		return models.ChatbotAIConnection{}, aiproviders.ErrInvalidRequest
	}
	encrypted, err := crypto.Encrypt(credential, s.key)
	if err != nil {
		return models.ChatbotAIConnection{}, ErrOff
	}
	c := models.ChatbotAIConnection{OrganizationID: org, Name: name, EndpointID: id, Model: model, Credential: encrypted, Enabled: true, Revision: 1, CredentialScope: s.endpoints[id].target.CredentialScope()}
	err = s.db.WithContext(ctx).Create(&c).Error
	return c, err
}

func validName(name string) bool {
	return name != "" && len(name) <= 100 && strings.IndexFunc(name, unicode.IsControl) == -1
}
func (s *Service) validEndpoint(id, model string) bool {
	ep, ok := s.endpoints[id]
	return ok && ep.target.AllowsModel(model)
}
func get(db *gorm.DB, org, id uuid.UUID) (models.ChatbotAIConnection, error) {
	var c models.ChatbotAIConnection
	err := db.Where("organization_id = ? AND id = ?", org, id).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrMissing
	}
	return c, err
}
func (s *Service) mutate(ctx context.Context, org, id uuid.UUID, apply func(*gorm.DB, models.ChatbotAIConnection) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		c, err := get(tx.Clauses(clause.Locking{Strength: "UPDATE"}), org, id)
		if err != nil {
			return err
		}
		return apply(tx, c)
	})
}
func checkUnassigned(tx *gorm.DB, c models.ChatbotAIConnection) error {
	var count int64
	if err := tx.Model(&models.ChatbotAIBinding{}).Where("organization_id = ? AND connection_id = ?", c.OrganizationID, c.ID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrAssigned
	}
	return nil
}

type Update struct {
	Name       *string `json:"name"`
	EndpointID *string `json:"endpoint_id"`
	Model      *string `json:"model"`
	Credential *string `json:"credential"`
	Enabled    *bool   `json:"enabled"`
}

func (s *Service) Update(ctx context.Context, org, id uuid.UUID, in Update) error {
	if !s.enabled {
		return ErrOff
	}
	return s.mutate(ctx, org, id, func(tx *gorm.DB, c models.ChatbotAIConnection) error {
		if in.Name != nil {
			c.Name = strings.TrimSpace(*in.Name)
		}
		if !validName(c.Name) {
			return aiproviders.ErrInvalidRequest
		}
		updates := map[string]any{"name": c.Name}
		if in.EndpointID != nil || in.Model != nil || in.Credential != nil {
			if err := checkUnassigned(tx, c); err != nil {
				return err
			}
			if in.EndpointID != nil && *in.EndpointID != c.EndpointID && in.Credential == nil {
				return aiproviders.ErrInvalidRequest
			}
			if in.EndpointID != nil {
				c.EndpointID = *in.EndpointID
			}
			if in.Model != nil {
				c.Model = *in.Model
			}
			if !s.validEndpoint(c.EndpointID, c.Model) {
				return aiproviders.ErrInvalidRequest
			}
			scope := s.endpoints[c.EndpointID].target.CredentialScope()
			if c.CredentialScope != scope && in.Credential == nil {
				return aiproviders.ErrInvalidRequest
			}
			updates["credential_scope"] = scope
			updates["endpoint_id"] = c.EndpointID
			updates["model"] = c.Model
			if in.Credential != nil {
				if !aiproviders.ValidCredential(*in.Credential) {
					return aiproviders.ErrInvalidRequest
				}
				encrypted, err := crypto.Encrypt(*in.Credential, s.key)
				if err != nil {
					return ErrOff
				}
				updates["credential"] = encrypted
			}
			updates["tested_policy"] = ""
			updates["tested_at"] = nil
			updates["revision"] = c.Revision + 1
		}
		if in.Enabled != nil && *in.Enabled != c.Enabled {
			updates["enabled"] = *in.Enabled
			updates["revision"] = c.Revision + 1
			updates["tested_policy"] = ""
			updates["tested_at"] = nil
		}
		return tx.Model(&c).Updates(updates).Error
	})
}

func (s *Service) Disable(ctx context.Context, org, id uuid.UUID) error {
	return s.mutate(ctx, org, id, func(tx *gorm.DB, c models.ChatbotAIConnection) error {
		return tx.Model(&c).Updates(map[string]any{"enabled": false, "revision": c.Revision + 1, "tested_policy": "", "tested_at": nil}).Error
	})
}

func (s *Service) Delete(ctx context.Context, org, id uuid.UUID) error {
	return s.mutate(ctx, org, id, func(tx *gorm.DB, c models.ChatbotAIConnection) error {
		if err := checkUnassigned(tx, c); err != nil {
			return err
		}
		return tx.Unscoped().Delete(&c).Error
	})
}

func (s *Service) Assign(ctx context.Context, org, account, id uuid.UUID, history, includeContext bool) error {
	db := s.db.WithContext(ctx)
	if id == uuid.Nil {
		return db.Where("organization_id = ? AND account_id = ?", org, account).Delete(&models.ChatbotAIBinding{}).Error
	}
	if !s.enabled {
		return ErrOff
	}
	return s.mutate(ctx, org, id, func(tx *gorm.DB, c models.ChatbotAIConnection) error {
		if !s.Ready(c) {
			return ErrNotReady
		}
		if account != uuid.Nil {
			var count int64
			if err := tx.Model(&models.WhatsAppAccount{}).Where("organization_id = ? AND id = ?", org, account).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return ErrMissing
			}
		}
		binding := models.ChatbotAIBinding{OrganizationID: org, AccountID: account, ConnectionID: id, IncludeHistory: history, IncludeContext: includeContext}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "organization_id"}, {Name: "account_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"connection_id", "include_history", "include_context"}),
		}).Create(&binding).Error
	})
}

func (s *Service) Binding(ctx context.Context, org, account uuid.UUID) (*models.ChatbotAIBinding, error) {
	var b models.ChatbotAIBinding
	err := s.db.WithContext(ctx).Where("organization_id = ? AND account_id IN ?", org, []uuid.UUID{account, uuid.Nil}).
		Order("account_id DESC").First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Service) claim(id uuid.UUID) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.busy) >= 2 || s.busy[id] {
		return nil, ErrBusy
	}
	s.busy[id] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.busy, id)
	}, nil
}

func (s *Service) call(ctx context.Context, c models.ChatbotAIConnection, maxTokens int, messages func() []aiproviders.Message) (aiproviders.Reply, error) {
	ep, ok := s.endpoints[c.EndpointID]
	if !s.enabled || !c.Enabled || !ok || !ep.target.AllowsModel(c.Model) || c.CredentialScope != ep.target.CredentialScope() {
		return aiproviders.Reply{}, ErrNotReady
	}
	if !crypto.IsEncrypted(c.Credential) {
		return aiproviders.Reply{}, ErrNotReady
	}
	key, err := crypto.Decrypt(c.Credential, s.key)
	if err != nil || !aiproviders.ValidCredential(key) {
		return aiproviders.Reply{}, ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, aiproviders.Timeout)
	defer cancel()
	return ep.provider.Generate(ctx, aiproviders.Request{Credential: key, Model: c.Model, MaxTokens: maxTokens, Messages: messages()})
}

func (s *Service) Probe(ctx context.Context, org, id uuid.UUID) (time.Duration, error) {
	release, err := s.claim(id)
	if err != nil {
		return 0, err
	}
	defer release()
	c, err := get(s.db.WithContext(ctx), org, id)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	_, err = s.call(ctx, c, 1024, func() []aiproviders.Message { return []aiproviders.Message{{Role: "user", Content: probePrompt}} })
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	result := s.db.WithContext(ctx).Model(&models.ChatbotAIConnection{}).
		Where("organization_id = ? AND id = ? AND revision = ? AND enabled", org, id, c.Revision).
		Updates(map[string]any{"tested_at": time.Now().UTC(), "tested_policy": s.endpoints[c.EndpointID].target.Fingerprint()})
	if result.Error != nil {
		return elapsed, result.Error
	}
	if result.RowsAffected != 1 {
		return elapsed, ErrChanged
	}
	return elapsed, nil
}

func (s *Service) Run(ctx context.Context, b models.ChatbotAIBinding, maxTokens int, messages func() []aiproviders.Message) (aiproviders.Reply, error) {
	release, err := s.claim(b.ConnectionID)
	if err != nil {
		return aiproviders.Reply{}, err
	}
	defer release()
	c, err := get(s.db.WithContext(ctx), b.OrganizationID, b.ConnectionID)
	if err != nil {
		return aiproviders.Reply{}, err
	}
	if !s.Ready(c) {
		return aiproviders.Reply{}, ErrNotReady
	}
	return s.call(ctx, c, maxTokens, messages)
}
