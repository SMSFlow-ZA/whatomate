package channelapp

import (
	"context"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"math/big"
	"regexp"
	"time"
)

var thresholdPattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,4})?$`)
var balancePattern = regexp.MustCompile(`^-?[0-9]{1,16}(\.[0-9]{1,8})?$`)

func ValidBalanceThreshold(value string) bool { return thresholdPattern.MatchString(value) }

// Credits and currency remain separate units. Unknown units never acquire an
// invented price or currency; zero/negative balances are still actionable.
func BalanceUpdates(balance *channels.Balance, threshold string, now time.Time) map[string]any {
	updates := map[string]any{"balance_checked_at": now, "low_balance": false}
	if balance == nil || !balancePattern.MatchString(balance.Amount) {
		updates["balance"] = models.JSONB{"unit": "unknown"}
		return updates
	}
	unit := balance.Unit
	if unit != "currency" && unit != "credits" {
		unit = "unknown"
	}
	value, _ := new(big.Rat).SetString(balance.Amount)
	limit, ok := new(big.Rat).SetString(threshold)
	if !ok {
		limit = new(big.Rat)
	}
	low := value.Sign() <= 0 || (unit != "unknown" && value.Cmp(limit) <= 0)
	currency := balance.Currency
	if len(currency) != 3 {
		currency = ""
	}
	updates["balance"] = models.JSONB{"amount": balance.Amount, "unit": unit, "currency": currency}
	updates["low_balance"] = low
	return updates
}

func (s *Service) RefreshBalances(ctx context.Context, now time.Time) error {
	var rows []models.ChannelConnection
	if err := s.DB.WithContext(ctx).Where("enabled = true AND validated_at IS NOT NULL AND (balance_checked_at IS NULL OR balance_checked_at < ?)", now.Add(-5*time.Minute)).Limit(20).Find(&rows).Error; err != nil {
		return err
	}
	for _, conn := range rows {
		p, c, err := s.runtime(&conn)
		if err != nil {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		info, err := p.Validate(callCtx, c)
		cancel()
		updates := BalanceUpdates(info.Balance, conn.BalanceThreshold, now)
		updates["balance_error_code"] = ""
		if err != nil || !ValidCapabilities(conn.Provider, info.Capabilities) {
			updates = map[string]any{"balance_checked_at": now, "balance_error_code": "validation_failed"}
		}
		if err := s.DB.WithContext(ctx).Model(&models.ChannelConnection{}).Where("id = ? AND updated_at = ?", conn.ID, conn.UpdatedAt).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}
