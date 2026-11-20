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
		var next time.Time
		// Weekly schedules repeat; AddDate preserves local dates across DST.
		for day := 1; day <= 7; day++ {
			date := local.AddDate(0, 0, day)
			for _, shift := range policy.Shifts {
				if shift.Day != date.Weekday() {
					continue
				}
				start := time.Date(date.Year(), date.Month(), date.Day(), shift.StartMinute/60, shift.StartMinute%60, 0, 0, location)
				if next.IsZero() || start.Before(next) {
					next = start
				}
			}
			if !next.IsZero() {
				break
			}
		}
		if !next.IsZero() {
			options = append(options, inboxSnoozeOption{"working_day", next, location.String()})
		}
	}
	return options
}
