// Package ai contains every prompt used in the app. Keeping all prompt
// engineering in one place makes it easy to tune, and keeps the HTTP
// handlers free of prompt strings.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"smartbook/internal/groq"
)

// BookingIntent is the structured result of parsing a free-text booking
// request such as "book me a haircut tomorrow around 3pm, I'm flexible
// on stylist".
type BookingIntent struct {
	ServiceKeyword     string `json:"service_keyword"`
	ProviderNameHint   string `json:"provider_name_hint"`
	DateISO            string `json:"date_iso"`            // YYYY-MM-DD, best guess
	TimeWindowStart    string `json:"time_window_start"`   // HH:MM 24h, best guess
	TimeWindowEnd      string `json:"time_window_end"`     // HH:MM 24h, best guess
	Flexibility        string `json:"flexibility"`         // short free text
	Confidence         string `json:"confidence"`          // "high" | "medium" | "low"
	ClarifyingQuestion string `json:"clarifying_question"` // non-empty only if confidence is low
}

func extractJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return raw
	}
	return raw[start : end+1]
}

// ParseBookingIntent turns free text into a structured booking intent.
// `nowRef` and `tz` anchor relative phrases like "tomorrow" or "next Friday".
func ParseBookingIntent(ctx context.Context, c *groq.Client, freeText string, nowRef time.Time, availableServices []string) (BookingIntent, error) {
	sys := fmt.Sprintf(`You are a booking intent parser for a scheduling app. Today's date is %s (%s), local time %s.
Extract the customer's intent as STRICT JSON with exactly these fields:
service_keyword, provider_name_hint, date_iso (YYYY-MM-DD), time_window_start (HH:MM 24h),
time_window_end (HH:MM 24h), flexibility, confidence ("high","medium", or "low"), clarifying_question.
Known services you can match against (best effort, fuzzy match allowed): %s.
If a field is unknown, use an empty string. Only set clarifying_question if confidence is "low".
Respond with ONLY the JSON object, no prose, no markdown fences.`,
		nowRef.Format("2006-01-02"), nowRef.Format("Monday"), nowRef.Format("15:04"),
		strings.Join(availableServices, ", "))

	raw, err := c.Complete(ctx, groq.Smart, sys, freeText, true)
	if err != nil {
		return BookingIntent{}, err
	}
	var intent BookingIntent
	if err := json.Unmarshal([]byte(extractJSON(raw)), &intent); err != nil {
		return BookingIntent{}, fmt.Errorf("ai: could not parse intent JSON: %w (raw=%s)", err, raw)
	}
	return intent, nil
}

// GenerateConfirmation drafts a short, warm confirmation message in the
// customer's preferred language.
func GenerateConfirmation(ctx context.Context, c *groq.Client, customerName, language, serviceName, providerName string, start time.Time) (string, error) {
	sys := fmt.Sprintf(`You write short, warm booking confirmation messages for a scheduling app.
Write in this language/locale: %s (use natural script and phrasing for that language; if "bn", write in Bengali).
Keep it under 60 words, no markdown, no emoji spam (at most one), include the date and time clearly.`, language)
	user := fmt.Sprintf("Customer: %s\nService: %s\nProvider: %s\nWhen: %s",
		customerName, serviceName, providerName, start.Format("Monday, 2 Jan 2006 at 3:04 PM"))
	return c.Complete(ctx, groq.Fast, sys, user, false)
}

// MediateConflict drafts a personalized message to the customer who lost a
// double-booked slot, offering the best available alternative and explaining
// the situation respectfully.
func MediateConflict(ctx context.Context, c *groq.Client, customerName, serviceName string, requestedStart, altStart, altEnd time.Time, flexibilityNote string) (string, error) {
	sys := `You are a courteous booking conflict mediator. Two customers requested the same slot;
one got it first. Write a short (under 80 words), empathetic message to the customer who did NOT
get the original slot, acknowledging the overlap, offering the alternative time clearly, and asking
them to confirm or propose another time. No markdown.`
	user := fmt.Sprintf(
		"Customer: %s\nService: %s\nOriginally requested: %s\nProposed alternative: %s to %s\nCustomer's stated flexibility: %q",
		customerName, serviceName,
		requestedStart.Format("Mon 2 Jan, 3:04 PM"),
		altStart.Format("Mon 2 Jan, 3:04 PM"), altEnd.Format("3:04 PM"),
		flexibilityNote,
	)
	return c.Complete(ctx, groq.Fast, sys, user, false)
}

// AssessNoShowRisk reasons over free-text booking history/notes and returns
// a short, plain-English risk note + suggested mitigation. This is an LLM
// judgment call, not a trained statistical model — the README says so.
func AssessNoShowRisk(ctx context.Context, c *groq.Client, customerName string, pastNotes []string, newBookingNotes string) (string, error) {
	sys := `You help a small business gauge no-show risk from booking notes/history. Given a short
history of free-text notes for a customer plus their note for the upcoming booking, respond with
ONE short line: a risk level (Low/Medium/High) and a 1-sentence reason and suggestion
(e.g. "send a reminder", "ask for a small deposit", "no action needed"). Under 35 words. No markdown.`
	user := fmt.Sprintf("Customer: %s\nPast notes: %s\nUpcoming booking note: %q",
		customerName, strings.Join(pastNotes, " | "), newBookingNotes)
	return c.Complete(ctx, groq.Fast, sys, user, false)
}

// SuggestPricing gives an advisory (non-binding, non-automatic) price
// suggestion based on how full the upcoming schedule is. Nothing charges
// automatically — this is decision support only.
func SuggestPricing(ctx context.Context, c *groq.Client, serviceName string, basePrice float64, currency string, fillRatePct int, daysOut int) (string, error) {
	sys := `You are a pricing advisor for a small service business. Suggest whether to keep, raise, or
lower the price for an upcoming time window, and by roughly how much (percentage), based on how full
the schedule already is. Be conservative and explain briefly. Under 45 words. No markdown.`
	user := fmt.Sprintf("Service: %s\nBase price: %.2f %s\nSchedule fill rate for the next %d day(s): %d%%",
		serviceName, basePrice, currency, daysOut, fillRatePct)
	return c.Complete(ctx, groq.Fast, sys, user, false)
}

// WeatherAdvisory turns a raw forecast into a friendly customer-facing note,
// only called when the weather package already flagged the day as risky for
// an outdoor booking.
func WeatherAdvisory(ctx context.Context, c *groq.Client, customerName, serviceName, weatherSummary string, precipProbability int, start time.Time) (string, error) {
	sys := `You write short, friendly weather advisories for outdoor service bookings. Mention the
forecast plainly, suggest the customer may want to reschedule or confirm they're fine with it, and
keep an easygoing, non-alarming tone. Under 55 words. No markdown.`
	user := fmt.Sprintf("Customer: %s\nService: %s\nWhen: %s\nForecast: %s, %d%% chance of precipitation",
		customerName, serviceName, start.Format("Mon 2 Jan, 3:04 PM"), weatherSummary, precipProbability)
	return c.Complete(ctx, groq.Fast, sys, user, false)
}

// RescheduleReply drafts a conversational reply once a new slot has been
// found for a customer's free-text reschedule request.
func RescheduleReply(ctx context.Context, c *groq.Client, customerName, serviceName, language string, newStart, newEnd time.Time) (string, error) {
	sys := fmt.Sprintf(`You write short, friendly rescheduling confirmations in this language/locale: %s
(use natural script for that language; if "bn", write in Bengali). Under 50 words, no markdown.`, language)
	user := fmt.Sprintf("Customer: %s\nService: %s\nNew time: %s to %s",
		customerName, serviceName, newStart.Format("Mon 2 Jan, 3:04 PM"), newEnd.Format("3:04 PM"))
	return c.Complete(ctx, groq.Fast, sys, user, false)
}
