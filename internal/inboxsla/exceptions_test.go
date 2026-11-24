package inboxsla

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func weekdays() []Shift {
	var shifts []Shift
	for day := time.Monday; day <= time.Friday; day++ {
		shifts = append(shifts, Shift{Day: day, StartMinute: 8 * 60, EndMinute: 17 * 60})
	}
	return shifts
}

func TestCalendarExceptionsReplaceRatherThanExtendWeeklyHours(t *testing.T) {
	clock, err := BusinessClockWithExceptions("Africa/Johannesburg", weekdays(), []DateException{
		{Date: "2026-11-23"}, // Monday closed.
		{Date: "2026-11-24", Intervals: []Interval{{StartMinute: 10 * 60, EndMinute: 12 * 60}}},
		{Date: "2026-11-21", Intervals: []Interval{{StartMinute: 9 * 60, EndMinute: 10 * 60}}},
	})
	require.NoError(t, err)
	start := utc(20, 14, 30) // Friday 16:30 local.
	want := utc(24, 8, 30)   // 30 Friday + 60 Saturday + 30 Tuesday.
	require.Equal(t, want, clock.Deadline(start, 2*time.Hour))
	require.Equal(t, 2*time.Hour, clock.Elapsed(start, want, nil))
	require.Equal(t, time.Duration(0), clock.Elapsed(utc(23, 0, 0), utc(24, 0, 0), nil))
	// Tuesday's ordinary 08:00 opening was replaced, not added.
	require.Equal(t, utc(24, 8, 30), clock.Deadline(utc(24, 6, 0), 30*time.Minute))
	// Explicit dates do not recur every Monday.
	require.Equal(t, utc(30, 7, 0), clock.Deadline(utc(30, 6, 0), time.Hour))
	require.Equal(t, utc(24, 8, 0), clock.NextWorkingTime(utc(23, 0, 0)))
	require.Equal(t, utc(21, 7, 0), clock.NextWorkingTime(utc(21, 0, 0)))
	require.Equal(t, utc(24, 9, 0), clock.NextWorkingTime(utc(24, 9, 0)))
	require.Equal(t, utc(25, 6, 0), clock.NextWorkingTime(utc(24, 10, 0)))
}

func TestCalendarExceptionIntervalsAreCopiedSortedAndAdjacent(t *testing.T) {
	shifts := weekdays()
	exceptions := []DateException{{Date: "2026-11-23", Intervals: []Interval{
		{StartMinute: 10 * 60, EndMinute: 11 * 60},
		{StartMinute: 9 * 60, EndMinute: 10 * 60},
	}}}
	clock, err := BusinessClockWithExceptions("UTC", shifts, exceptions)
	require.NoError(t, err)
	shifts[0].StartMinute = 0
	exceptions[0].Intervals[0].EndMinute = 24 * 60
	exceptions[0].Date = "2026-11-24"
	require.Equal(t, utc(23, 11, 0), clock.Deadline(utc(23, 9, 0), 2*time.Hour))
	require.Equal(t, 2*time.Hour, clock.Elapsed(utc(23, 0, 0), utc(24, 0, 0), nil))
	require.Equal(t, 9*time.Hour, clock.Elapsed(utc(30, 0, 0), utc(31, 0, 0), nil))
}

func TestCalendarExceptionInvalidInputs(t *testing.T) {
	cases := map[string][]DateException{
		"nonexistent date":   {{Date: "2026-02-29"}},
		"non ISO date":       {{Date: "2026-1-02"}},
		"timestamp":          {{Date: "2026-11-23T00:00:00Z"}},
		"zero year":          {{Date: "0000-01-01"}},
		"duplicate closed":   {{Date: "2026-11-23"}, {Date: "2026-11-23"}},
		"negative":           {{Date: "2026-11-23", Intervals: []Interval{{StartMinute: -1, EndMinute: 60}}}},
		"empty interval":     {{Date: "2026-11-23", Intervals: []Interval{{StartMinute: 60, EndMinute: 60}}}},
		"cross midnight":     {{Date: "2026-11-23", Intervals: []Interval{{StartMinute: 23 * 60, EndMinute: 60}}}},
		"beyond day":         {{Date: "2026-11-23", Intervals: []Interval{{StartMinute: 0, EndMinute: 1441}}}},
		"overlap":            {{Date: "2026-11-23", Intervals: []Interval{{StartMinute: 60, EndMinute: 180}, {StartMinute: 0, EndMinute: 120}}}},
		"too many intervals": {{Date: "2026-11-23", Intervals: make([]Interval, 29)}},
		"too many dates":     make([]DateException, 367),
	}
	for name, exceptions := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := BusinessClockWithExceptions("UTC", weekdays(), exceptions)
			require.Error(t, err)
		})
	}
	_, err := BusinessClockWithExceptions("UTC", nil, []DateException{{Date: "2026-11-23", Intervals: []Interval{{StartMinute: 0, EndMinute: 60}}}})
	require.Error(t, err, "finite openings cannot replace the recurring schedule")
}

func TestCalendarExceptionDaylightSavingUsesActualUTCIntervals(t *testing.T) {
	clock, err := BusinessClockWithExceptions("America/New_York", []Shift{{Day: time.Monday, StartMinute: 8 * 60, EndMinute: 17 * 60}}, []DateException{
		{Date: "2026-03-08", Intervals: []Interval{{StartMinute: 60, EndMinute: 180}}},
		{Date: "2026-11-01", Intervals: []Interval{{StartMinute: 60, EndMinute: 180}}},
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		date  string
		hours int
	}{{"2026-03-08", 1}, {"2026-11-01", 3}} {
		t.Run(tc.date, func(t *testing.T) {
			start, err := time.Parse(time.RFC3339, tc.date+"T00:00:00Z")
			require.NoError(t, err)
			due := clock.Deadline(start, time.Duration(tc.hours)*time.Hour)
			require.Equal(t, time.Duration(tc.hours)*time.Hour, clock.Elapsed(start, due, nil))
			require.Equal(t, time.Duration(tc.hours)*time.Hour, clock.Elapsed(start, start.Add(24*time.Hour), nil))
			require.Equal(t, clock.Deadline(start, time.Nanosecond).Add(-time.Nanosecond), clock.NextWorkingTime(start))
		})
	}
}

func TestCalendarExceptionYearBoundaryAndPauses(t *testing.T) {
	clock, err := BusinessClockWithExceptions("UTC", weekdays(), []DateException{{Date: "2026-12-31"}, {Date: "2027-01-01"}})
	require.NoError(t, err)
	start := time.Date(2026, 12, 30, 16, 0, 0, 0, time.UTC)
	want := time.Date(2027, 1, 4, 9, 0, 0, 0, time.UTC)
	require.Equal(t, want, clock.Deadline(start, 2*time.Hour))
	pauses := []Pause{{Start: start.Add(30 * time.Minute), End: want.Add(-30 * time.Minute)}, {Start: start.Add(45 * time.Minute), End: want}}
	require.Equal(t, 30*time.Minute, clock.Elapsed(start, want, pauses))
	for minutes := 1; minutes <= 1800; minutes += 37 {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			budget := time.Duration(minutes) * time.Minute
			require.Equal(t, budget, clock.Elapsed(start, clock.Deadline(start, budget), nil))
		})
	}
}

func TestCalendarWithoutExceptionsPreservesLegacyClock(t *testing.T) {
	legacy, err := BusinessClock("Africa/Johannesburg", weekdays())
	require.NoError(t, err)
	newClock, err := BusinessClockWithExceptions("Africa/Johannesburg", weekdays(), []DateException{})
	require.NoError(t, err)
	for _, minutes := range []int{0, 1, 59, 540, 541, 10000} {
		start := utc(20, 14, 30)
		budget := time.Duration(minutes) * time.Minute
		require.Equal(t, legacy.Deadline(start, budget), newClock.Deadline(start, budget))
		require.Equal(t, legacy.Elapsed(start, utc(30, 16, 0), nil), newClock.Elapsed(start, utc(30, 16, 0), nil))
	}
}
