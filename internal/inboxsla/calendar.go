package inboxsla

import (
	"errors"
	"slices"
	"time"
)

// Shift is a half-open local time interval within one weekday. A shift that
// crosses midnight is represented by one interval per calendar day.
type Shift struct {
	Day         time.Weekday
	StartMinute int
	EndMinute   int
}

// Interval is a half-open local wall-time interval on a particular date.
type Interval struct {
	StartMinute int `json:"start_minute"`
	EndMinute   int `json:"end_minute"`
}

// DateException replaces the complete weekly schedule for this local date.
// No intervals means closed; exceptions do not recur in subsequent years.
type DateException struct {
	Date      string     `json:"date"`
	Intervals []Interval `json:"intervals"`
}

type Pause struct {
	Start time.Time
	End   time.Time // Zero means the case is still waiting for the customer.
}

type Clock struct {
	location   *time.Location
	shifts     [7][]Shift
	exceptions map[string][]Shift
}

// ElapsedClock counts all elapsed time, including weekends.
func ElapsedClock() Clock { return Clock{} }

func BusinessClock(zone string, shifts []Shift) (Clock, error) {
	return BusinessClockWithExceptions(zone, shifts, nil)
}

// BusinessClockWithExceptions preserves weekly behaviour outside the explicitly
// named local dates. Finite exceptions and a nonempty weekly schedule ensure
// Deadline can always advance after the last exception.
func BusinessClockWithExceptions(zone string, shifts []Shift, exceptions []DateException) (Clock, error) {
	if zone == "" || zone == "Local" || len(shifts) == 0 || len(shifts) > 28 {
		return Clock{}, errors.New("a timezone and business schedule are required")
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return Clock{}, err
	}
	clock := Clock{location: location}
	for _, shift := range shifts {
		if shift.Day < time.Sunday || shift.Day > time.Saturday || shift.StartMinute < 0 ||
			shift.StartMinute >= shift.EndMinute || shift.EndMinute > 24*60 {
			return Clock{}, errors.New("invalid business hours")
		}
		clock.shifts[shift.Day] = append(clock.shifts[shift.Day], shift)
	}
	for day := range clock.shifts {
		slices.SortFunc(clock.shifts[day], func(a, b Shift) int { return a.StartMinute - b.StartMinute })
		for i := 1; i < len(clock.shifts[day]); i++ {
			if clock.shifts[day][i].StartMinute < clock.shifts[day][i-1].EndMinute {
				return Clock{}, errors.New("business hours overlap")
			}
		}
	}
	if len(exceptions) > 366 {
		return Clock{}, errors.New("at most 366 calendar date exceptions are permitted")
	}
	clock.exceptions = make(map[string][]Shift, len(exceptions))
	for _, exception := range exceptions {
		date, err := time.Parse("2006-01-02", exception.Date)
		if err != nil || date.Year() < 1 || date.Format("2006-01-02") != exception.Date || len(exception.Intervals) > 28 {
			return Clock{}, errors.New("invalid calendar date exception")
		}
		if _, exists := clock.exceptions[exception.Date]; exists {
			return Clock{}, errors.New("calendar date exceptions must be unique")
		}
		intervals := make([]Shift, 0, len(exception.Intervals))
		for _, interval := range exception.Intervals {
			if interval.StartMinute < 0 || interval.StartMinute >= interval.EndMinute || interval.EndMinute > 24*60 {
				return Clock{}, errors.New("invalid calendar exception hours")
			}
			intervals = append(intervals, Shift{Day: date.Weekday(), StartMinute: interval.StartMinute, EndMinute: interval.EndMinute})
		}
		slices.SortFunc(intervals, func(a, b Shift) int { return a.StartMinute - b.StartMinute })
		for i := 1; i < len(intervals); i++ {
			if intervals[i].StartMinute < intervals[i-1].EndMinute {
				return Clock{}, errors.New("calendar exception hours overlap")
			}
		}
		clock.exceptions[exception.Date] = intervals
	}
	return clock, nil
}

// Elapsed sums scheduled time and excludes intervals in which the resolution
// clock was paused. Overlapping pause records are counted once.
func (c Clock) Elapsed(start, end time.Time, pauses []Pause) time.Duration {
	if !start.Before(end) {
		return 0
	}
	ordered := slices.Clone(pauses)
	slices.SortFunc(ordered, func(a, b Pause) int { return a.Start.Compare(b.Start) })
	cursor := start
	var total time.Duration
	for _, pause := range ordered {
		if !pause.Start.Before(end) {
			break
		}
		pauseEnd := pause.End
		if pauseEnd.IsZero() || pauseEnd.After(end) {
			pauseEnd = end
		}
		if !pause.Start.Before(pauseEnd) || !cursor.Before(pauseEnd) {
			continue
		}
		if cursor.Before(pause.Start) {
			total += c.scheduledBetween(cursor, pause.Start)
		}
		cursor = pauseEnd
	}
	return total + c.scheduledBetween(cursor, end)
}

// Deadline advances through scheduled working time, returning a UTC instant.
func (c Clock) Deadline(start time.Time, budget time.Duration) time.Time {
	if budget <= 0 || c.location == nil {
		return start.Add(max(budget, 0)).UTC()
	}
	for day := civilDate(start.In(c.location)); ; day = day.AddDate(0, 0, 1) {
		for _, interval := range c.intervals(day) {
			open, close := interval.Start, interval.End
			if open.Before(start) {
				open = start
			}
			available := close.Sub(open)
			if available <= 0 {
				continue
			}
			if budget <= available {
				return open.Add(budget).UTC()
			}
			budget -= available
		}
	}
}

// NextWorkingTime returns start if it is inside an open interval, otherwise
// the next opening. It uses the same calendar and DST intervals as deadlines.
func (c Clock) NextWorkingTime(start time.Time) time.Time {
	if c.location == nil {
		return start.UTC()
	}
	for day := civilDate(start.In(c.location)); ; day = day.AddDate(0, 0, 1) {
		for _, interval := range c.intervals(day) {
			if start.Before(interval.End) {
				if start.After(interval.Start) {
					return start.UTC()
				}
				return interval.Start.UTC()
			}
		}
	}
}

func (c Clock) scheduledBetween(start, end time.Time) time.Duration {
	if !start.Before(end) {
		return 0
	}
	if c.location == nil {
		return end.Sub(start)
	}
	var total time.Duration
	last := civilDate(end.In(c.location))
	for day := civilDate(start.In(c.location)); !day.After(last); day = day.AddDate(0, 0, 1) {
		for _, interval := range c.intervals(day) {
			open, close := interval.Start, interval.End
			if open.Before(start) {
				open = start
			}
			if close.After(end) {
				close = end
			}
			if open.Before(close) {
				total += close.Sub(open)
			}
		}
	}
	return total
}

func civilDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Build the actual UTC intervals using the zone in effect in each segment.
// Repeated wall times can contribute twice, skipped minutes contribute zero,
// and separate shifts stay separate through either transition.
func (c Clock) intervals(day time.Time) []Pause {
	shifts := c.shifts[day.Weekday()]
	if replacement, exists := c.exceptions[day.Format("2006-01-02")]; exists {
		shifts = replacement
	}
	if len(shifts) == 0 {
		return nil
	}
	cursor := day.Add(-24 * time.Hour)
	limit := day.Add(48 * time.Hour)
	intervals := make([]Pause, 0, len(shifts))
	for cursor.Before(limit) {
		zoned := cursor.In(c.location)
		_, offset := zoned.Zone()
		_, transition := zoned.ZoneBounds()
		if transition.IsZero() || transition.After(limit) {
			transition = limit
		}
		for _, shift := range shifts {
			open := day.Add(time.Duration(shift.StartMinute)*time.Minute - time.Duration(offset)*time.Second)
			close := day.Add(time.Duration(shift.EndMinute)*time.Minute - time.Duration(offset)*time.Second)
			if open.Before(cursor) {
				open = cursor
			}
			if close.After(transition) {
				close = transition
			}
			if open.Before(close) {
				intervals = append(intervals, Pause{Start: open, End: close})
			}
		}
		cursor = transition
	}
	return intervals
}
