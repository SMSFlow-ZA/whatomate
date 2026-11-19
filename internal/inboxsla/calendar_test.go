package inboxsla

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func utc(day, hour, minute int) time.Time {
	return time.Date(2026, time.November, day, hour, minute, 0, 0, time.UTC)
}

func TestBusinessClockWeekendAndPauses(t *testing.T) {
	clock, err := BusinessClock("Africa/Johannesburg", []Shift{
		{Day: time.Friday, StartMinute: 8 * 60, EndMinute: 17 * 60},
		{Day: time.Monday, StartMinute: 8 * 60, EndMinute: 17 * 60},
	})
	require.NoError(t, err)
	start := utc(20, 14, 30)
	require.Equal(t, utc(23, 7, 30), clock.Deadline(start, 2*time.Hour))
	end := utc(23, 8, 0)
	require.Equal(t, 150*time.Minute, clock.Elapsed(start, end, nil))
	require.Equal(t, 75*time.Minute, clock.Elapsed(start, end, []Pause{
		{Start: utc(23, 6, 0), End: utc(23, 7, 0)},
		{Start: utc(20, 14, 45), End: utc(23, 6, 30)},
	}))
	require.Equal(t, 30*time.Minute, clock.Elapsed(start, end, []Pause{{Start: utc(20, 15, 0)}}))
}

func TestBusinessClockDaylightSaving(t *testing.T) {
	clock, err := BusinessClock("America/New_York", []Shift{{Day: time.Sunday, StartMinute: 60, EndMinute: 180}})
	require.NoError(t, err)
	spring := time.Date(2026, time.March, 8, 5, 0, 0, 0, time.UTC)
	fall := time.Date(2026, time.November, 1, 4, 0, 0, 0, time.UTC)
	require.Equal(t, time.Hour, clock.Elapsed(spring, spring.Add(5*time.Hour), nil))
	require.Equal(t, 3*time.Hour, clock.Elapsed(fall, fall.Add(5*time.Hour), nil))
	require.Equal(t, fall.Add(4*time.Hour), clock.Deadline(fall, 3*time.Hour))
}

func TestElapsedClockIgnoresOpeningHours(t *testing.T) {
	start := utc(20, 14, 0)
	clock := ElapsedClock()
	require.Equal(t, start.Add(time.Hour), clock.Deadline(start, time.Hour))
	require.Equal(t, 71*time.Hour, clock.Elapsed(start, start.Add(72*time.Hour), []Pause{
		{Start: start.Add(time.Hour), End: start.Add(2 * time.Hour)},
	}))
}
