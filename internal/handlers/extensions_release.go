package handlers

import (
	"fmt"
	"strings"

	"github.com/shridarpatil/whatomate/internal/config"
)

func validateExtensionConversationConfig(c config.ExtensionsConfig) error {
	if (!c.SyntheticDevelopment && !c.ProtectedMode) || c.RosterFreshnessSeconds <= 0 || c.RosterFreshnessSeconds > 31*86400 || c.EscalationAfterSeconds <= 0 || c.EscalationAfterSeconds > 365*86400 || strings.TrimSpace(c.ConversationEmployeeAudience) == "" || strings.Contains(c.ConversationEmployeeAudience, "*") || strings.TrimSpace(c.ConversationEmployeeAudience) != c.ConversationEmployeeAudience {
		return fmt.Errorf("conversation requires explicit protected eligibility, audience and bounded timing configuration")
	}
	return nil
}

// A dedicated ER host admits only its complete, explicit production composition.
// Provider validation and persistent database protection are separate mandatory
// steps; passing this configuration check alone never activates protected mode.
func validateProtectedExtensionSettings(c config.ExtensionsConfig) error {
	if !c.ProtectedMode || c.SyntheticDevelopment || c.AIProvider != "https" || c.MFAFreshnessSeconds <= 0 || c.MFAFreshnessSeconds > 3600 || c.ReportsMinGroup < 2 {
		return fmt.Errorf("protected ER release requires production AI, fresh MFA and disclosure policy")
	}
	if err := validateExtensionConversationConfig(c); err != nil {
		return err
	}
	modules := map[string]bool{}
	for _, id := range extensionIDs(c.Modules) {
		modules[id] = true
	}
	for _, id := range []string{"directory", "cases", "knowledge", "ai", "conversation", "reports", "governance"} {
		if !modules[id] {
			return fmt.Errorf("protected ER release missing compiled module: %s", id)
		}
	}
	if c.CarrierEnabled {
		eligible := false
		for _, id := range extensionIDs(c.RequiredInbound) {
			if id == "directory" {
				eligible = true
			}
		}
		if !eligible {
			return fmt.Errorf("protected carrier requires current directory eligibility")
		}
	}
	return nil
}

// A configured demonstration allowlist applies even with production providers.
// Eligibility, current case permission and carrier window checks still follow.
func extensionCarrierRecipientAllowed(c config.ExtensionsConfig, number string) bool {
	if !c.SyntheticDevelopment && len(c.CarrierAllowedRecipients) == 0 {
		return true
	}
	for _, allowed := range c.CarrierAllowedRecipients {
		if allowed == number {
			return true
		}
	}
	return false
}
