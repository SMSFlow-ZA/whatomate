package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"time"
)

type inboxSnoozeOption struct {
	Key      string    `json:"key"`
	Until    time.Time `json:"until"`
	Timezone string    `json:"timezone"`
}

// Suggestions use the case's immutable calendar, never the operator's browser
// timezone or a newly edited policy. Selection still requires explicit Save.
func inboxSnoozeOptions(policy models.InboxSLAPolicy, now time.Time) []inboxSnoozeOption {
	location, err := time.LoadLocation(policy.Timezone)
	if err != nil {
		location = time.UTC
	}
	local := now.In(location)
	options := []inboxSnoozeOption{{"later", now.Add(2 * time.Hour), location.String()}}
	tomorrow := local.AddDate(0, 0, 1)
	options = append(options, inboxSnoozeOption{"tomorrow", time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 9, 0, 0, 0, location), location.String()})
	if policy.BusinessHours {
		if clock, err := inboxPolicyClock(policy); err == nil {
			midnight := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, location)
			next := clock.NextWorkingTime(midnight).In(location)
			options = append(options, inboxSnoozeOption{"working_day", next, location.String()})
		}
	}
	return options
}
