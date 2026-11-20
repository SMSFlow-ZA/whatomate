package inboxquality

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/stretchr/testify/require"
)

func TestDuePreservesExpiredDeadlineAndMergesPauses(t *testing.T) {
	start := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	clock := inboxsla.ElapsedClock()
	cases := []struct {
		name   string
		pauses []inboxsla.Pause
		want   *time.Time
	}{
		{"no pause", nil, ptr(start.Add(time.Hour))},
		{"waiting", []inboxsla.Pause{{Start: start.Add(10 * time.Minute)}}, nil},
		{"resumed", []inboxsla.Pause{{Start: start.Add(10 * time.Minute), End: start.Add(30 * time.Minute)}}, ptr(start.Add(80 * time.Minute))},
		{"overlap", []inboxsla.Pause{{Start: start.Add(10 * time.Minute), End: start.Add(30 * time.Minute)}, {Start: start.Add(20 * time.Minute), End: start.Add(40 * time.Minute)}}, ptr(start.Add(90 * time.Minute))},
		{"pause after breach", []inboxsla.Pause{{Start: start.Add(2 * time.Hour)}}, ptr(start.Add(time.Hour))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := Due(clock, start, time.Hour, tc.pauses)
			if tc.want == nil {
				require.Nil(t, actual)
			} else {
				require.NotNil(t, actual)
				require.Equal(t, *tc.want, *actual)
			}
		})
	}
}
func ptr(value time.Time) *time.Time { return &value }

func TestDueUsesBusinessCalendarAcrossWeekend(t *testing.T) {
	var shifts []inboxsla.Shift
	for day := time.Monday; day <= time.Friday; day++ {
		shifts = append(shifts, inboxsla.Shift{Day: day, StartMinute: 8 * 60, EndMinute: 17 * 60})
	}
	clock, err := inboxsla.BusinessClock("Africa/Johannesburg", shifts)
	require.NoError(t, err)
	friday := time.Date(2026, 11, 20, 14, 50, 0, 0, time.UTC)
	got := Due(clock, friday, 30*time.Minute, nil)
	require.NotNil(t, got)
	require.Equal(t, time.Date(2026, 11, 23, 6, 20, 0, 0, time.UTC), *got)
}
