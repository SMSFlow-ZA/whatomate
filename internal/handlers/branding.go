package handlers

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const logoMax = 2 * 1024 * 1024
const iconMax = 512 * 1024

var accentPattern = regexp.MustCompile("^#[a-fA-F0-9]{6}$")

type brandDisplay struct {
	DisplayName string `json:"display_name"`
	AccentColor string `json:"accent_color"`
	LogoURL     string `json:"logo_url"`
	FaviconURL  string `json:"favicon_url"`
	Custom      bool   `json:"custom"`
}

func (a *App) brandingDisplay(row models.InstanceBranding) brandDisplay {
	base := strings.TrimRight(a.Config.Server.BasePath, "/")
	display := brandDisplay{DisplayName: "SMSFlow", AccentColor: "#00AEEF", LogoURL: base + "/branding/smsflow-logo.png", FaviconURL: base + "/branding/smsflow-favicon.svg"}
	if row.ID == 1 {
		display.DisplayName = row.DisplayName
		display.AccentColor = row.AccentColor
		display.Custom = true
		if row.LogoHash != "" {
			display.LogoURL = base + "/api/branding/assets/logo?v=" + row.LogoHash
		}
		if row.FaviconHash != "" {
			display.FaviconURL = base + "/api/branding/assets/favicon?v=" + row.FaviconHash
		}
	}
	return display
}
func brandingRow(db *gorm.DB) (models.InstanceBranding, error) {
	var row models.InstanceBranding
	err := db.First(&row, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return row, err
}
func (a *App) GetBranding(r *fastglue.Request) error {
	var row models.InstanceBranding
	err := plan.ReadWithStorageLock(a.DB, []string{a.getMediaStoragePath(), a.getAudioDir()}, func(db *gorm.DB) error { var err error; row, err = brandingRow(db); return err })
	if err != nil {
		return r.SendErrorEnvelope(503, "Branding temporarily unavailable", nil, "")
	}
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	return r.SendEnvelope(a.brandingDisplay(row))
}
func normalizeBrandImage(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
		return nil, fmt.Errorf("use a valid PNG/JPEG image up to 2048 by 2048 pixels")
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("image cannot be decoded")
	}
	var out bytes.Buffer
	err = png.Encode(&out, im)
	return out.Bytes(), err
}
func (a *App) brandingPath(asset string) string {
	return filepath.Join(a.getMediaStoragePath(), "branding", asset+".png")
}
func (a *App) UpdateBranding(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsBranding, models.ActionWrite)
	if err != nil {
		return nil
	}
	if len(r.RequestCtx.PostBody()) > 3*1024*1024 {
		return r.SendErrorEnvelope(400, "Branding upload is too large", nil, "")
	}
	form, err := r.RequestCtx.MultipartForm()
	if err != nil {
		return r.SendErrorEnvelope(400, "Use multipart branding settings", nil, "")
	}
	defer r.RequestCtx.Request.RemoveMultipartFormFiles()
	value := func(key string) string {
		values := form.Value[key]
		if len(values) != 1 {
			return ""
		}
		return values[0]
	}
	name := strings.TrimSpace(value("display_name"))
	accent := value("accent_color")
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) || !accentPattern.MatchString(accent) {
		return r.SendErrorEnvelope(400, "Use a display name of 1–80 characters and a six-digit hex colour", nil, "")
	}
	updates := map[string][]byte{}
	removals := map[string]bool{}
	changes := []plan.FileChange{}
	for _, asset := range []string{"logo", "favicon"} {
		files := form.File[asset]
		remove := value("remove_"+asset) == "true"
		if len(files) > 1 || (remove && len(files) > 0) {
			return r.SendErrorEnvelope(400, "Choose one image or remove it", nil, "")
		}
		if remove {
			removals[asset] = true
			changes = append(changes, plan.FileChange{Path: a.brandingPath(asset), Delete: true})
			continue
		}
		if len(files) == 0 {
			continue
		}
		limit := int64(logoMax)
		if asset == "favicon" {
			limit = iconMax
		}
		if files[0].Size > limit {
			return r.SendErrorEnvelope(400, "Logo limit is 2 MiB; favicon limit is 512 KiB", nil, "")
		}
		reader, err := files[0].Open()
		if err != nil {
			return r.SendErrorEnvelope(400, "Cannot read image", nil, "")
		}
		data, err := io.ReadAll(io.LimitReader(reader, limit+1))
		_ = reader.Close()
		if err != nil || int64(len(data)) > limit {
			return r.SendErrorEnvelope(400, "Image upload is too large", nil, "")
		}
		normalized, err := normalizeBrandImage(data)
		if err != nil {
			return r.SendErrorEnvelope(400, err.Error(), nil, "")
		}
		updates[asset] = normalized
		changes = append(changes, plan.FileChange{Path: a.brandingPath(asset), Data: normalized})
	}
	var before, after models.InstanceBranding
	err = plan.WriteFilesWithCommit(a.DB, a.Config.Plan, []string{a.getMediaStoragePath(), a.getAudioDir()}, changes, func(tx *gorm.DB) error {
		var err error
		before, err = brandingRow(tx)
		if err != nil {
			return err
		}
		after = before
		after.ID = 1
		after.DisplayName = name
		after.AccentColor = strings.ToUpper(accent)
		for _, asset := range []string{"logo", "favicon"} {
			hash := ""
			if data, ok := updates[asset]; ok {
				hash = fmt.Sprintf("%x", sha256.Sum256(data))
			} else if !removals[asset] {
				continue
			}
			if asset == "logo" {
				after.LogoHash = hash
			} else {
				after.FaviconHash = hash
			}
		}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&after).Error
	})
	if errors.Is(err, plan.ErrStorageLimit) {
		return r.SendErrorEnvelope(409, "Storage allowance reached", map[string]any{"code": "plan_limit_reached", "resource": "storage"}, "")
	}
	if err != nil {
		return r.SendErrorEnvelope(500, "Branding save could not be confirmed; reload before retrying", nil, "")
	}
	audit.LogAudit(a.DB, org, user, audit.GetUserName(a.DB, user), models.ResourceSettingsBranding, org, models.AuditActionUpdated, brandAudit(before), brandAudit(after))
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	return r.SendEnvelope(a.brandingDisplay(after))
}
func brandAudit(row models.InstanceBranding) map[string]any {
	return map[string]any{"display_name": row.DisplayName, "accent_color": row.AccentColor, "logo_hash": row.LogoHash, "favicon_hash": row.FaviconHash}
}
func (a *App) ResetBranding(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsBranding, models.ActionWrite)
	if err != nil {
		return nil
	}
	var before models.InstanceBranding
	changes := []plan.FileChange{{Path: a.brandingPath("logo"), Delete: true}, {Path: a.brandingPath("favicon"), Delete: true}}
	err = plan.WriteFilesWithCommit(a.DB, a.Config.Plan, []string{a.getMediaStoragePath(), a.getAudioDir()}, changes, func(tx *gorm.DB) error {
		var err error
		before, err = brandingRow(tx)
		if err != nil {
			return err
		}
		return tx.Delete(&models.InstanceBranding{}, 1).Error
	})
	if err != nil {
		return r.SendErrorEnvelope(500, "Branding reset could not be confirmed; reload before retrying", nil, "")
	}
	audit.LogAudit(a.DB, org, user, audit.GetUserName(a.DB, user), models.ResourceSettingsBranding, org, models.AuditActionUpdated, brandAudit(before), brandAudit(models.InstanceBranding{}))
	return a.GetBranding(r)
}
func (a *App) BrandingAsset(r *fastglue.Request) error {
	asset, _ := r.RequestCtx.UserValue("asset").(string)
	if asset != "logo" && asset != "favicon" {
		return r.SendErrorEnvelope(404, "Image not found", nil, "")
	}
	var data []byte
	var hash string
	err := plan.ReadWithStorageLock(a.DB, []string{a.getMediaStoragePath(), a.getAudioDir()}, func(db *gorm.DB) error {
		row, err := brandingRow(db)
		if err != nil {
			return err
		}
		hash = row.LogoHash
		if asset == "favicon" {
			hash = row.FaviconHash
		}
		if hash == "" || string(r.RequestCtx.QueryArgs().Peek("v")) != hash {
			return os.ErrNotExist
		}
		// os.Root prevents uploaded symlinks from escaping the uploads tree.
		root, err := os.OpenRoot(a.getMediaStoragePath())
		if err != nil {
			return err
		}
		defer root.Close()
		f, err := root.Open(filepath.Join("branding", asset+".png"))
		if err != nil {
			return err
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, 32*1024*1024+1))
		if err != nil {
			return err
		}
		if len(data) > 32*1024*1024 || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
			return os.ErrNotExist
		}
		return nil
	})
	if err != nil {
		return r.SendErrorEnvelope(404, "Image not found", nil, "")
	}
	r.RequestCtx.Response.Header.SetContentType("image/png")
	r.RequestCtx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	r.RequestCtx.Response.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
	r.RequestCtx.SetBody(data)
	return nil
}
