package models

// InstanceBranding is presentation-only and intentionally independent of org names.
type InstanceBranding struct {
	ID          int    `gorm:"primaryKey;check:branding_singleton,id = 1" json:"-"`
	DisplayName string `gorm:"size:320;not null" json:"display_name"`
	AccentColor string `gorm:"size:7;not null" json:"accent_color"`
	LogoHash    string `gorm:"size:64" json:"-"`
	FaviconHash string `gorm:"size:64" json:"-"`
}
