// Package engine contains the deterministic, non-AI scheduling logic:
// working hours, overlap checks, and next-free-slot search. Keeping this
// separate from the AI layer means the app's actual booking correctness
// never depends on an LLM being available or right.
package engine

import (
	"time"

	"smartbook/internal/models"
	"smartbook/internal/store"
)

const (
	WorkStartHour = 9
	WorkEndHour   = 18
	stepMinutes   = 15
)

// ClampToWorkingHours pushes a desired start time forward into the next
// valid working window if it falls outside 09:00-18:00, or to the next
// day's opening time if it's after hours.
func ClampToWorkingHours(t time.Time) time.Time {
	start := time.Date(t.Year(), t.Month(), t.Day(), WorkStartHour, 0, 0, 0, t.Location())
	end := time.Date(t.Year(), t.Month(), t.Day(), WorkEndHour, 0, 0, 0, t.Location())
	if t.Before(start) {
		return start
	}
	if !t.Before(end) {
		return start.AddDate(0, 0, 1)
	}
	return t
}

// IsFree reports whether [start,end) is free of active bookings for the
// given provider.
func IsFree(s *store.Store, providerID string, start, end time.Time) bool {
	return len(s.ActiveBookingsOverlapping(providerID, start, end)) == 0
}

// FindSlot looks for the first available [start,end) window of the given
// duration for a provider, beginning at desiredStart, searching forward in
// 15-minute increments across working hours for up to maxDays days.
// It returns ok=false if nothing was found in that window.
func FindSlot(s *store.Store, providerID string, desiredStart time.Time, durationMin, maxDays int) (start, end time.Time, ok bool) {
	cursor := ClampToWorkingHours(desiredStart)
	deadline := cursor.AddDate(0, 0, maxDays)
	dur := time.Duration(durationMin) * time.Minute

	for cursor.Before(deadline) {
		dayEnd := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), WorkEndHour, 0, 0, 0, cursor.Location())
		if cursor.Add(dur).After(dayEnd) {
			// move to next day's opening time
			cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day()+1, WorkStartHour, 0, 0, 0, cursor.Location())
			continue
		}
		candidateEnd := cursor.Add(dur)
		if IsFree(s, providerID, cursor, candidateEnd) {
			return cursor, candidateEnd, true
		}
		cursor = cursor.Add(stepMinutes * time.Minute)
	}
	return time.Time{}, time.Time{}, false
}

// FillRatePct estimates how full a provider's schedule is over the next N
// days, as a rough percentage of working-hour capacity already booked.
// This is plain arithmetic, not AI — the AI layer only turns the number
// into human-readable pricing advice.
func FillRatePct(s *store.Store, providerID string, from time.Time, days int) int {
	totalCapacityMin := days * (WorkEndHour - WorkStartHour) * 60
	if totalCapacityMin <= 0 {
		return 0
	}
	until := from.AddDate(0, 0, days)
	bookedMin := 0
	for _, b := range s.ActiveBookingsOverlapping(providerID, from, until) {
		if b.Status == models.StatusCancelled {
			continue
		}
		d := int(b.End.Sub(b.Start).Minutes())
		if d > 0 {
			bookedMin += d
		}
	}
	pct := (bookedMin * 100) / totalCapacityMin
	if pct > 100 {
		pct = 100
	}
	return pct
}
