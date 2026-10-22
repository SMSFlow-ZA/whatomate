package config

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SHA-1 is mandated by the coturn TURN REST API (RFC draft)
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config holds all configuration for the application
type Config struct {
	App          AppConfig          `koanf:"app"`
	Server       ServerConfig       `koanf:"server"`
	Database     DatabaseConfig     `koanf:"database"`
	Redis        RedisConfig        `koanf:"redis"`
	JWT          JWTConfig          `koanf:"jwt"`
	WhatsApp     WhatsAppConfig     `koanf:"whatsapp"`
	AI           AIConfig           `koanf:"ai"`
	Storage      StorageConfig      `koanf:"storage"`
	DefaultAdmin DefaultAdminConfig `koanf:"default_admin"`
	RateLimit    RateLimitConfig    `koanf:"rate_limit"`
	Cookie       CookieConfig       `koanf:"cookie"`
	Calling      CallingConfig      `koanf:"calling"`
	TTS          TTSConfig          `koanf:"tts"`
	Plan         PlanConfig         `koanf:"plan"`
	Channels     ChannelsConfig     `koanf:"channels"`
	Extensions   ExtensionsConfig   `koanf:"extensions"`
}

// ExtensionsConfig is trusted host configuration, never writable through APIs.
// ProtectedMode requires the complete reviewed composition and persists a
// monotonic database boundary; ordinary mode cannot reopen that database.
// ExtensionCarrierBinding is an operator-owned exact account binding. Secret
// values are never returned by the extension catalog or configuration APIs.
type ExtensionCarrierBinding struct {
	OrganizationID string `koanf:"organization_id"`
	AccountID      string `koanf:"account_id"`
	PhoneID        string `koanf:"phone_id"`
	AppSecret      string `koanf:"app_secret"`
}

// ExtensionCarrierSMSBinding reuses an existing validated compiled SMS provider
// connection. Binding is operator-owned; generic users cannot alter this schedule.
type ExtensionCarrierSMSBinding struct {
	OrganizationID    string `koanf:"organization_id"`
	ConnectionID      string `koanf:"connection_id"`
	WhatsAppAccountID string `koanf:"whatsapp_account_id"`
}

type ExtensionsConfig struct {
	CarrierEnabled         bool                         `koanf:"carrier_enabled"`
	CarrierDispatchEnabled bool                         `koanf:"carrier_dispatch_enabled"`
	CarrierBindings        []ExtensionCarrierBinding    `koanf:"carrier_bindings"`
	CarrierSMSBindings     []ExtensionCarrierSMSBinding `koanf:"carrier_sms_bindings"`
	// Synthetic profiles require an explicit canonical recipient allowlist.
	CarrierAllowedRecipients []string `koanf:"carrier_allowed_recipients"`

	Required        string `koanf:"required"`
	RequiredInbound string `koanf:"required_inbound"`
	ProtectedMode   bool   `koanf:"protected_mode"`
	// SecurityKey is a base64-encoded 32-byte secret supplied by the operator.
	SecurityKey                string `koanf:"security_key"`
	MFAFreshnessSeconds        int    `koanf:"mfa_freshness_seconds"`
	Modules                    string `koanf:"modules"`
	RosterFreshnessSeconds     int    `koanf:"roster_freshness_seconds"`
	SyntheticDevelopment       bool   `koanf:"synthetic_development"`
	DevelopmentAIEndpoint      string `koanf:"development_ai_endpoint"`
	DevelopmentModelDigest     string `koanf:"development_model_digest"`
	DevelopmentEmbeddingDigest string `koanf:"development_embedding_digest"`
	// Production AI is an explicit operator-owned HTTPS deployment. Country and
	// attestation identify reviewed evidence; enabling a module invents neither.
	AIProvider                          string   `koanf:"ai_provider"`
	AIEndpoint                          string   `koanf:"ai_endpoint"`
	AIEmbeddingEndpoint                 string   `koanf:"ai_embedding_endpoint"`
	AIAllowedEndpoints                  []string `koanf:"ai_allowed_endpoints"`
	AIDeploymentAttestation             string   `koanf:"ai_deployment_attestation"`
	AICountry                           string   `koanf:"ai_country"`
	AIModel                             string   `koanf:"ai_model"`
	AIEmbeddingModel                    string   `koanf:"ai_embedding_model"`
	AIPromptRevision                    string   `koanf:"ai_prompt_revision"`
	AIAPIKey                            string   `koanf:"ai_api_key"`
	AIEmbeddingDimensions               int      `koanf:"ai_embedding_dimensions"`
	AIProcessingScope                   string   `koanf:"ai_processing_scope"`
	AIInternationalProcessingApproval   string   `koanf:"ai_international_processing_approval"`
	AIInternationalProcessingDisclosure string   `koanf:"ai_international_processing_disclosure"`
	KnowledgeVectorEnabled              bool     `koanf:"knowledge_vector_enabled"`
	KnowledgeVectorIdentity             string   `koanf:"knowledge_vector_identity"`
	ReportsMinGroup                     int      `koanf:"reports_min_group"`
	ConversationEmployeeAudience        string   `koanf:"conversation_employee_audience"`
	EscalationAfterSeconds              int      `koanf:"escalation_after_seconds"`
	EscalationWorkerIDs                 string   `koanf:"escalation_worker_ids"`
	RoutingPrimaryIDs                   string   `koanf:"routing_primary_ids"`
	RoutingBackupIDs                    string   `koanf:"routing_backup_ids"`
}

type ChannelsConfig struct {
	StopDispatch       bool   `koanf:"stop_dispatch"`
	SMSFlowEndpoint    string `koanf:"smsflow_endpoint"`     // trusted host setting; HTTPS origin only
	SMSFlowReplyRoutes string `koanf:"smsflow_reply_routes"` // comma-separated deployment-verified two-way routes
}

type TTSConfig struct {
	PiperBinary   string `koanf:"piper_binary"`   // path to piper executable
	PiperModel    string `koanf:"piper_model"`    // path to .onnx voice model
	OpusencBinary string `koanf:"opusenc_binary"` // path to opusenc (defaults to "opusenc")
}

// defaultTURNCredentialTTLSecs is the lifetime of generated coturn REST
// credentials when an ICE server has a secret but no explicit credential_ttl.
const defaultTURNCredentialTTLSecs = 86400 // 24h

type ICEServerConfig struct {
	URLs       []string `koanf:"urls"`
	Username   string   `koanf:"username"`
	Credential string   `koanf:"credential"`
	// Secret enables coturn's "use-auth-secret" (TURN REST API) mode. When set,
	// short-lived credentials are generated per request instead of using the
	// static Username/Credential above.
	Secret string `koanf:"secret"`
	// CredentialTTL is the lifetime (seconds) of generated credentials. Defaults
	// to defaultTURNCredentialTTLSecs when <= 0. Only used when Secret is set.
	CredentialTTL int `koanf:"credential_ttl"`
}

// ResolveCredentials returns the username and credential to advertise for this
// ICE server. When Secret is set it derives short-lived coturn REST credentials
// (use-auth-secret): the username is "<expiry-unix>" — optionally prefixed onto
// any configured Username as "<expiry-unix>:<username>" — and the credential is
// the base64-encoded HMAC-SHA1 of that username keyed by the secret. When Secret
// is empty, the static Username/Credential are returned unchanged.
func (s ICEServerConfig) ResolveCredentials(now time.Time) (username, credential string) {
	if s.Secret == "" {
		return s.Username, s.Credential
	}

	ttl := s.CredentialTTL
	if ttl <= 0 {
		ttl = defaultTURNCredentialTTLSecs
	}

	expiry := now.Add(time.Duration(ttl) * time.Second).Unix()
	username = strconv.FormatInt(expiry, 10)
	if s.Username != "" {
		username = username + ":" + s.Username
	}

	mac := hmac.New(sha1.New, []byte(s.Secret))
	mac.Write([]byte(username))
	credential = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return username, credential
}

type CallingConfig struct {
	MaxCallDuration     int               `koanf:"max_call_duration"`
	AudioDir            string            `koanf:"audio_dir"`
	HoldMusicFile       string            `koanf:"hold_music_file"`
	TransferTimeoutSecs int               `koanf:"transfer_timeout_secs"`
	PerAgentTimeoutSecs int               `koanf:"per_agent_timeout_secs"`
	StickyRingSecs      int               `koanf:"sticky_ring_secs"`
	RingbackFile        string            `koanf:"ringback_file"`
	UDPPortMin          uint16            `koanf:"udp_port_min"` // WebRTC UDP port range start (default: 10000)
	UDPPortMax          uint16            `koanf:"udp_port_max"` // WebRTC UDP port range end (default: 10100)
	PublicIP            string            `koanf:"public_ip"`    // Public IP for NAT mapping (required on AWS/cloud)
	RelayOnly           bool              `koanf:"relay_only"`   // Force all media through TURN relay (no direct UDP)
	ICEServers          []ICEServerConfig `koanf:"ice_servers"`
	RecordingEnabled    bool              `koanf:"recording_enabled"` // Enable call recording to S3
}

type AppConfig struct {
	Name          string `koanf:"name"`
	Environment   string `koanf:"environment"` // development, staging, production
	Debug         bool   `koanf:"debug"`
	EncryptionKey string `koanf:"encryption_key"` // AES-256 key for encrypting secrets at rest
}

type ServerConfig struct {
	Host           string `koanf:"host"`
	Port           int    `koanf:"port"`
	ReadTimeout    int    `koanf:"read_timeout"`
	WriteTimeout   int    `koanf:"write_timeout"`
	BasePath       string `koanf:"base_path"`       // Base path for frontend (e.g., "/whatomate" for proxy pass)
	AllowedOrigins string `koanf:"allowed_origins"` // Comma-separated list of allowed CORS origins
}

type DatabaseConfig struct {
	// Externally migrated protected releases verify a compiled catalog contract;
	// runtime never receives schema ownership or permission to install migrations.
	SchemaManagedExternally bool   `koanf:"schema_managed_externally"`
	SchemaRelease           string `koanf:"schema_release"`
	SchemaSHA256            string `koanf:"schema_sha256"`
	Host                    string `koanf:"host"`
	Port                    int    `koanf:"port"`
	User                    string `koanf:"user"`
	Password                string `koanf:"password"`
	Name                    string `koanf:"name"`
	SSLMode                 string `koanf:"ssl_mode"`
	MaxOpenConns            int    `koanf:"max_open_conns"`
	MaxIdleConns            int    `koanf:"max_idle_conns"`
	ConnMaxLifetime         int    `koanf:"conn_max_lifetime"`
}

type RedisConfig struct {
	Host     string `koanf:"host"`
	Port     int    `koanf:"port"`
	Username string `koanf:"username"`
	Password string `koanf:"password"`
	DB       int    `koanf:"db"`
	TLS      bool   `koanf:"tls"`
}

type JWTConfig struct {
	Secret            string `koanf:"secret"`
	AccessExpiryMins  int    `koanf:"access_expiry_mins"`
	RefreshExpiryDays int    `koanf:"refresh_expiry_days"`
}

type WhatsAppConfig struct {
	WebhookSubscriptionOverrides []WhatsAppWebhookSubscriptionOverride `koanf:"webhook_subscription_overrides"`
	WebhookVerifyToken           string                                `koanf:"webhook_verify_token"`
	APIVersion                   string                                `koanf:"api_version"`
	BaseURL                      string                                `koanf:"base_url"` // Meta Graph API base URL
	AppID                        string                                `koanf:"app_id"`   // WhatsApp App ID for frontend
	AppSecret                    string                                `koanf:"app_secret"`
	ConfigID                     string                                `koanf:"config_id"` // WhatsApp Config ID for frontend
}

// Webhook subscription routing is trusted operator configuration, bound to the
// exact current organization, account UUID and WABA. Never accept it in an API body.
type WhatsAppWebhookSubscriptionOverride struct {
	OrganizationID string `koanf:"organization_id"`
	AccountID      string `koanf:"account_id"`
	BusinessID     string `koanf:"business_id"`
	CallbackURL    string `koanf:"callback_url"`
}

type AIConfig struct {
	OpenAIKey    string `koanf:"openai_key"`
	AnthropicKey string `koanf:"anthropic_key"`
	GoogleKey    string `koanf:"google_key"`
}

type StorageConfig struct {
	Type      string `koanf:"type"` // local, s3
	LocalPath string `koanf:"local_path"`
	S3Bucket  string `koanf:"s3_bucket"`
	S3Region  string `koanf:"s3_region"`
	S3Key     string `koanf:"s3_key"`
	S3Secret  string `koanf:"s3_secret"`
}

type DefaultAdminConfig struct {
	Email    string `koanf:"email"`
	Password string `koanf:"password"`
	FullName string `koanf:"full_name"`
}

type CookieConfig struct {
	Domain string `koanf:"domain"` // Cookie domain (e.g., ".example.com"). Empty = current host.
	Secure bool   `koanf:"secure"` // Set Secure flag. Auto-set true when environment=production.
}

type RateLimitConfig struct {
	Enabled             bool `koanf:"enabled"`
	LoginMaxAttempts    int  `koanf:"login_max_attempts"`
	RegisterMaxAttempts int  `koanf:"register_max_attempts"`
	RefreshMaxAttempts  int  `koanf:"refresh_max_attempts"`
	SSOMaxAttempts      int  `koanf:"sso_max_attempts"`
	WindowSeconds       int  `koanf:"window_seconds"`
	TrustProxy          bool `koanf:"trust_proxy"`
	APIMaxRequests      int  `koanf:"api_max_requests"`
	APIWindowSeconds    int  `koanf:"api_window_seconds"`
}

// Load loads configuration from file and environment variables
func Load(configPath string) (*Config, error) {
	k := koanf.New(".")

	// Load from config file if provided
	if configPath != "" {
		if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
			return nil, err
		}
	}

	// Load from environment variables (WHATOMATE_ prefix). A DOUBLE underscore
	// separates config levels; single underscores are preserved as part of the
	// key. This is required because both section and field names contain
	// underscores (e.g. default_admin, rate_limit, whatsapp.app_id) — collapsing
	// every "_" to "." would mangle them (whatsapp.app_id -> whatsapp.app.id), so
	// those keys could never be set via env.
	// e.g. WHATOMATE_DATABASE__HOST -> database.host
	//      WHATOMATE_WHATSAPP__APP_ID -> whatsapp.app_id
	//      WHATOMATE_DEFAULT_ADMIN__EMAIL -> default_admin.email
	if err := k.Load(env.Provider("WHATOMATE_", ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "WHATOMATE_")), "__", ".")
	}), nil); err != nil {
		return nil, err
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	// Set defaults
	setDefaults(&cfg)
	if err := ValidatePlan(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func setDefaults(cfg *Config) {
	if cfg.App.Name == "" {
		cfg.App.Name = "Whatomate"
	}
	if cfg.App.Environment == "" {
		cfg.App.Environment = "development"
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 30
	}
	if cfg.Database.Port == 0 {
		cfg.Database.Port = 5432
	}
	if cfg.Database.SSLMode == "" {
		cfg.Database.SSLMode = "disable"
	}
	if cfg.Database.MaxOpenConns == 0 {
		cfg.Database.MaxOpenConns = 25
	}
	if cfg.Database.MaxIdleConns == 0 {
		cfg.Database.MaxIdleConns = 5
	}
	if cfg.Database.ConnMaxLifetime == 0 {
		cfg.Database.ConnMaxLifetime = 300
	}
	if cfg.Redis.Port == 0 {
		cfg.Redis.Port = 6379
	}
	if cfg.JWT.AccessExpiryMins == 0 {
		cfg.JWT.AccessExpiryMins = 15
	}
	if cfg.JWT.RefreshExpiryDays == 0 {
		cfg.JWT.RefreshExpiryDays = 1
	}
	if cfg.WhatsApp.APIVersion == "" {
		cfg.WhatsApp.APIVersion = "v18.0"
	}
	if cfg.WhatsApp.BaseURL == "" {
		cfg.WhatsApp.BaseURL = "https://graph.facebook.com"
	}
	if cfg.Storage.Type == "" {
		cfg.Storage.Type = "local"
	}
	if cfg.Storage.LocalPath == "" {
		cfg.Storage.LocalPath = "./uploads"
	}
	// Default admin credentials (only used during initial setup)
	if cfg.DefaultAdmin.Email == "" {
		cfg.DefaultAdmin.Email = "admin@admin.com"
	}
	if cfg.DefaultAdmin.Password == "" {
		cfg.DefaultAdmin.Password = "admin"
	}
	if cfg.DefaultAdmin.FullName == "" {
		cfg.DefaultAdmin.FullName = "Admin"
	}
	// Cookie defaults
	if cfg.App.Environment == "production" {
		cfg.Cookie.Secure = true
	}
	// Rate limiting defaults
	if cfg.RateLimit.LoginMaxAttempts == 0 {
		cfg.RateLimit.LoginMaxAttempts = 10
	}
	if cfg.RateLimit.RegisterMaxAttempts == 0 {
		cfg.RateLimit.RegisterMaxAttempts = 10
	}
	if cfg.RateLimit.RefreshMaxAttempts == 0 {
		cfg.RateLimit.RefreshMaxAttempts = 30
	}
	if cfg.RateLimit.SSOMaxAttempts == 0 {
		cfg.RateLimit.SSOMaxAttempts = 10
	}
	if cfg.RateLimit.WindowSeconds == 0 {
		cfg.RateLimit.WindowSeconds = 60
	}
	// Calling defaults
	if cfg.Calling.MaxCallDuration == 0 {
		cfg.Calling.MaxCallDuration = 300
	}
	if cfg.Calling.AudioDir == "" {
		cfg.Calling.AudioDir = "./audio"
	}
	if cfg.Calling.HoldMusicFile == "" {
		cfg.Calling.HoldMusicFile = "hold_music.opus"
	}
	if cfg.Calling.TransferTimeoutSecs == 0 {
		cfg.Calling.TransferTimeoutSecs = 120
	}
}
