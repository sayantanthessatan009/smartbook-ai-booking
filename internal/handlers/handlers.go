// Package handlers wires the HTTP API. It is the only place that combines
// the deterministic engine, the AI layer, and the weather lookup — each of
// those packages stays independently testable and independently optional.
package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"smartbook/internal/ai"
	"smartbook/internal/engine"
	"smartbook/internal/groq"
	"smartbook/internal/models"
	"smartbook/internal/store"
	"smartbook/internal/weather"
)

type App struct {
	Store   *store.Store
	Groq    *groq.Client
	Weather *weather.Client
	// DefaultLat/Lon are used for outdoor weather advisories when a
	// provider hasn't set explicit coordinates.
	DefaultLat, DefaultLon float64
}

func NewMux(a *App) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", a.health)

	mux.HandleFunc("POST /api/providers", a.createProvider)
	mux.HandleFunc("GET /api/providers", a.listProviders)

	mux.HandleFunc("POST /api/services", a.createService)
	mux.HandleFunc("GET /api/services", a.listServices)

	mux.HandleFunc("POST /api/customers", a.upsertCustomer)
	mux.HandleFunc("GET /api/customers", a.listCustomers)

	mux.HandleFunc("POST /api/bookings", a.createBooking)
	mux.HandleFunc("GET /api/bookings", a.listBookings)
	mux.HandleFunc("GET /api/bookings/{id}", a.getBooking)
	mux.HandleFunc("POST /api/bookings/{id}/cancel", a.cancelBooking)

	mux.HandleFunc("POST /api/conflicts/{id}/accept", a.acceptConflict)
	mux.HandleFunc("POST /api/conflicts/{id}/decline", a.declineConflict)

	mux.HandleFunc("POST /api/ai/book", a.aiBook)
	mux.HandleFunc("POST /api/ai/reschedule", a.aiReschedule)
	mux.HandleFunc("GET /api/ai/pricing-suggestion", a.aiPricingSuggestion)

	return mux
}

// ---------- small helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status":          "ok",
		"time":            time.Now().Format(time.RFC3339),
		"groq_configured": a.Groq.IsConfigured(),
	})
}

// ---------- providers ----------

func (a *App) createProvider(w http.ResponseWriter, r *http.Request) {
	var p models.Provider
	if err := decode(r, &p); err != nil {
		writeErr(w, 400, "invalid body: "+err.Error())
		return
	}
	if p.Name == "" {
		writeErr(w, 400, "name is required")
		return
	}
	if p.Latitude == 0 && p.Longitude == 0 {
		p.Latitude, p.Longitude = a.DefaultLat, a.DefaultLon
	}
	out, err := a.Store.CreateProvider(p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, out)
}

func (a *App) listProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.Store.ListProviders())
}

// ---------- services ----------

func (a *App) createService(w http.ResponseWriter, r *http.Request) {
	var sv models.Service
	if err := decode(r, &sv); err != nil {
		writeErr(w, 400, "invalid body: "+err.Error())
		return
	}
	if sv.ProviderID == "" || sv.Name == "" || sv.DurationMin <= 0 {
		writeErr(w, 400, "provider_id, name, and duration_min (>0) are required")
		return
	}
	if _, ok := a.Store.GetProvider(sv.ProviderID); !ok {
		writeErr(w, 404, "provider not found")
		return
	}
	if sv.Currency == "" {
		sv.Currency = "INR"
	}
	out, err := a.Store.CreateService(sv)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, out)
}

func (a *App) listServices(w http.ResponseWriter, r *http.Request) {
	if pid := r.URL.Query().Get("provider_id"); pid != "" {
		writeJSON(w, 200, a.Store.ListServicesByProvider(pid))
		return
	}
	writeJSON(w, 200, a.Store.ListServices())
}

// ---------- customers ----------

func (a *App) upsertCustomer(w http.ResponseWriter, r *http.Request) {
	var c models.Customer
	if err := decode(r, &c); err != nil {
		writeErr(w, 400, "invalid body: "+err.Error())
		return
	}
	if c.Name == "" {
		writeErr(w, 400, "name is required")
		return
	}
	out, err := a.Store.UpsertCustomer(c)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, out)
}

func (a *App) listCustomers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.Store.ListCustomers())
}

// ---------- bookings (structured path) ----------

type createBookingReq struct {
	ProviderID  string `json:"provider_id"`
	ServiceID   string `json:"service_id"`
	CustomerID  string `json:"customer_id"`
	Start       string `json:"start"` // RFC3339
	Notes       string `json:"notes"`
	Flexibility string `json:"flexibility"`
}

func (a *App) createBooking(w http.ResponseWriter, r *http.Request) {
	var req createBookingReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid body: "+err.Error())
		return
	}
	svc, ok := a.Store.GetService(req.ServiceID)
	if !ok {
		writeErr(w, 404, "service not found")
		return
	}
	if req.ProviderID == "" {
		req.ProviderID = svc.ProviderID
	}
	prov, ok := a.Store.GetProvider(req.ProviderID)
	if !ok {
		writeErr(w, 404, "provider not found")
		return
	}
	cust, ok := a.Store.GetCustomer(req.CustomerID)
	if !ok {
		writeErr(w, 404, "customer not found")
		return
	}
	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		writeErr(w, 400, "start must be RFC3339, e.g. 2026-07-05T15:00:00+05:30")
		return
	}

	result := a.bookOrMediate(r.Context(), prov, svc, cust, start, req.Notes, req.Flexibility)
	writeJSON(w, 200, result)
}

// bookingOutcome is what every booking path (structured + AI) returns.
type bookingOutcome struct {
	Booking  *models.Booking          `json:"booking,omitempty"`
	Conflict *models.ConflictProposal `json:"conflict,omitempty"`
	Message  string                   `json:"message"`
}

// bookOrMediate is the shared core: try the exact slot; if taken, find the
// next free slot, put the new booking on it as "waitlisted", and raise an
// AI-mediated conflict proposal offering that alternative. This is the
// "AI conflict mediator" feature described in the README.
func (a *App) bookOrMediate(ctx context.Context, prov models.Provider, svc models.Service, cust models.Customer, desiredStart time.Time, notes, flexibility string) bookingOutcome {
	desiredStart = engine.ClampToWorkingHours(desiredStart)
	desiredEnd := desiredStart.Add(time.Duration(svc.DurationMin) * time.Minute)

	if engine.IsFree(a.Store, prov.ID, desiredStart, desiredEnd) {
		b, _ := a.Store.CreateBooking(models.Booking{
			ProviderID:  prov.ID,
			ServiceID:   svc.ID,
			CustomerID:  cust.ID,
			Start:       desiredStart,
			End:         desiredEnd,
			Status:      models.StatusConfirmed,
			Notes:       notes,
			Flexibility: flexibility,
		})
		a.enrichBookingAsync(&b, prov, svc, cust)
		return bookingOutcome{Booking: &b, Message: "Booked."}
	}

	// Slot taken — find the nearest alternative and raise a mediated conflict.
	altStart, altEnd, ok := engine.FindSlot(a.Store, prov.ID, desiredStart.Add(15*time.Minute), svc.DurationMin, 7)
	if !ok {
		return bookingOutcome{Message: "That slot is taken and no alternative was found in the next 7 days. Try a different date."}
	}

	b, _ := a.Store.CreateBooking(models.Booking{
		ProviderID:  prov.ID,
		ServiceID:   svc.ID,
		CustomerID:  cust.ID,
		Start:       altStart,
		End:         altEnd,
		Status:      models.StatusWaitlisted,
		Notes:       notes,
		Flexibility: flexibility,
	})

	msg := defaultConflictMessage(cust.Name, svc.Name, desiredStart, altStart, altEnd)
	if a.Groq.IsConfigured() {
		if aiMsg, err := ai.MediateConflict(ctx, a.Groq, cust.Name, svc.Name, desiredStart, altStart, altEnd, flexibility); err == nil {
			msg = aiMsg
		} else {
			log.Printf("ai mediate conflict fallback: %v", err)
		}
	}

	conflict, _ := a.Store.CreateConflict(models.ConflictProposal{
		LosingBookingID: b.ID,
		Message:         msg,
		SuggestedStart:  altStart,
		SuggestedEnd:    altEnd,
		Status:          "pending",
	})

	return bookingOutcome{Booking: &b, Conflict: &conflict, Message: "Requested slot was taken; an alternative has been proposed."}
}

func defaultConflictMessage(name, svc string, requested, altStart, altEnd time.Time) string {
	return "Hi " + name + ", the " + requested.Format("3:04 PM Jan 2") + " slot for " + svc +
		" was just taken. The next available time is " + altStart.Format("3:04 PM Jan 2") +
		" to " + altEnd.Format("3:04 PM") + ". Let us know if that works!"
}

// enrichBookingAsync fires the non-critical AI/weather enrichments
// (confirmation text, no-show risk note, weather advisory) synchronously
// but tolerates any of them failing without affecting the booking itself.
// Kept as a plain function (not a goroutine) to keep the demo's behavior
// predictable and easy to trace in logs; swap to `go a.enrichBooking(...)`
// for a fire-and-forget version in production.
func (a *App) enrichBookingAsync(b *models.Booking, prov models.Provider, svc models.Service, cust models.Customer) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if a.Groq.IsConfigured() {
		if msg, err := ai.GenerateConfirmation(ctx, a.Groq, cust.Name, cust.Language, svc.Name, prov.Name, b.Start); err == nil {
			b.Confirmation = msg
		} else {
			log.Printf("ai confirmation fallback: %v", err)
		}

		history := pastNotesForCustomer(a.Store, cust.ID, b.ID)
		if risk, err := ai.AssessNoShowRisk(ctx, a.Groq, cust.Name, history, b.Notes); err == nil {
			b.RiskNote = risk
		}
	}
	if b.Confirmation == "" {
		b.Confirmation = "Confirmed: " + svc.Name + " with " + prov.Name + " on " + b.Start.Format("Mon 2 Jan, 3:04 PM") + "."
	}

	if prov.Outdoor {
		lat, lon := prov.Latitude, prov.Longitude
		if fc, err := a.Weather.ForecastForDate(ctx, lat, lon, b.Start); err == nil && fc.IsBadForOutdoor() {
			note := fc.Summary + ", " + strconv.Itoa(fc.PrecipProbability) + "% chance of precipitation — consider rescheduling."
			if a.Groq.IsConfigured() {
				if aiNote, err := ai.WeatherAdvisory(ctx, a.Groq, cust.Name, svc.Name, fc.Summary, fc.PrecipProbability, b.Start); err == nil {
					note = aiNote
				}
			}
			b.WeatherNote = note
		}
	}

	_, _ = a.Store.UpdateBooking(*b)
}

func pastNotesForCustomer(s *store.Store, customerID, excludeBookingID string) []string {
	out := []string{}
	for _, b := range s.ListBookings() {
		if b.CustomerID == customerID && b.ID != excludeBookingID && b.Notes != "" {
			out = append(out, string(b.Status)+": "+b.Notes)
		}
	}
	return out
}

func (a *App) listBookings(w http.ResponseWriter, r *http.Request) {
	all := a.Store.ListBookings()
	if pid := r.URL.Query().Get("provider_id"); pid != "" {
		filtered := []models.Booking{}
		for _, b := range all {
			if b.ProviderID == pid {
				filtered = append(filtered, b)
			}
		}
		writeJSON(w, 200, filtered)
		return
	}
	writeJSON(w, 200, all)
}

func (a *App) getBooking(w http.ResponseWriter, r *http.Request) {
	b, ok := a.Store.GetBooking(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, b)
}

func (a *App) cancelBooking(w http.ResponseWriter, r *http.Request) {
	b, ok := a.Store.GetBooking(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = decode(r, &body)
	b.Status = models.StatusCancelled
	if body.Reason != "" {
		b.Notes = strings.TrimSpace(b.Notes + " | cancelled: " + body.Reason)
	}
	out, err := a.Store.UpdateBooking(b)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---------- conflict resolution ----------

func (a *App) acceptConflict(w http.ResponseWriter, r *http.Request) {
	c, ok := a.Store.GetConflict(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "conflict not found")
		return
	}
	b, ok := a.Store.GetBooking(c.LosingBookingID)
	if !ok {
		writeErr(w, 404, "booking not found")
		return
	}
	b.Status = models.StatusConfirmed
	b.Start, b.End = c.SuggestedStart, c.SuggestedEnd
	updated, _ := a.Store.UpdateBooking(b)

	c.Status = "accepted"
	_, _ = a.Store.UpdateConflict(c)

	writeJSON(w, 200, updated)
}

func (a *App) declineConflict(w http.ResponseWriter, r *http.Request) {
	c, ok := a.Store.GetConflict(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "conflict not found")
		return
	}
	b, ok := a.Store.GetBooking(c.LosingBookingID)
	if ok {
		b.Status = models.StatusCancelled
		_, _ = a.Store.UpdateBooking(b)
	}
	c.Status = "declined"
	updated, _ := a.Store.UpdateConflict(c)
	writeJSON(w, 200, updated)
}

// ---------- AI-native endpoints ----------

type aiBookReq struct {
	CustomerName    string `json:"customer_name"`
	CustomerContact string `json:"customer_contact"`
	Language        string `json:"language"`
	Text            string `json:"text"`
	ProviderID      string `json:"provider_id,omitempty"` // optional: narrow search to one provider
}

// aiBook is the natural-language booking endpoint: "book me a haircut
// tomorrow around 3pm, I'm flexible on stylist".
func (a *App) aiBook(w http.ResponseWriter, r *http.Request) {
	if !a.Groq.IsConfigured() {
		writeErr(w, 503, "AI booking requires GROQ_API_KEY to be set on the server")
		return
	}
	var req aiBookReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid body: "+err.Error())
		return
	}
	if req.CustomerName == "" || req.Text == "" {
		writeErr(w, 400, "customer_name and text are required")
		return
	}

	services := a.Store.ListServices()
	if req.ProviderID != "" {
		services = a.Store.ListServicesByProvider(req.ProviderID)
	}
	if len(services) == 0 {
		writeErr(w, 409, "no services exist yet — create a provider and service first")
		return
	}

	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, s.Name)
	}

	ctx := r.Context()
	intent, err := ai.ParseBookingIntent(ctx, a.Groq, req.Text, time.Now(), names)
	if err != nil {
		writeErr(w, 502, "AI parsing failed: "+err.Error())
		return
	}
	if intent.Confidence == "low" || intent.ServiceKeyword == "" {
		q := intent.ClarifyingQuestion
		if q == "" {
			q = "Could you clarify which service, date, and time you'd like?"
		}
		writeJSON(w, 200, map[string]string{"clarifying_question": q})
		return
	}

	svc, ok := matchService(services, intent.ServiceKeyword, intent.ProviderNameHint, a.Store)
	if !ok {
		writeJSON(w, 200, map[string]string{"clarifying_question": "I couldn't match that to a service we offer. Could you name it more specifically?"})
		return
	}
	prov, _ := a.Store.GetProvider(svc.ProviderID)

	start := resolveIntentTime(intent, time.Now())

	cust, err := a.Store.UpsertCustomer(models.Customer{
		Name:     req.CustomerName,
		Contact:  req.CustomerContact,
		Language: coalesce(req.Language, "en"),
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	result := a.bookOrMediate(ctx, prov, svc, cust, start, "booked via AI assistant: "+req.Text, intent.Flexibility)
	writeJSON(w, 200, result)
}

type aiRescheduleReq struct {
	BookingID string `json:"booking_id"`
	Text      string `json:"text"`
}

func (a *App) aiReschedule(w http.ResponseWriter, r *http.Request) {
	if !a.Groq.IsConfigured() {
		writeErr(w, 503, "AI reschedule requires GROQ_API_KEY to be set on the server")
		return
	}
	var req aiRescheduleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid body: "+err.Error())
		return
	}
	b, ok := a.Store.GetBooking(req.BookingID)
	if !ok {
		writeErr(w, 404, "booking not found")
		return
	}
	svc, _ := a.Store.GetService(b.ServiceID)
	prov, _ := a.Store.GetProvider(b.ProviderID)
	cust, _ := a.Store.GetCustomer(b.CustomerID)

	ctx := r.Context()
	intent, err := ai.ParseBookingIntent(ctx, a.Groq, req.Text, time.Now(), []string{svc.Name})
	if err != nil {
		writeErr(w, 502, "AI parsing failed: "+err.Error())
		return
	}
	desired := resolveIntentTime(intent, time.Now())

	newStart, newEnd, ok := engine.FindSlot(a.Store, prov.ID, desired, svc.DurationMin, 14)
	if !ok {
		writeErr(w, 409, "no free slot found in the next 14 days for that request")
		return
	}

	b.Start, b.End = newStart, newEnd
	b.Status = models.StatusConfirmed
	b.Notes = strings.TrimSpace(b.Notes + " | reschedule request: " + req.Text)
	updated, _ := a.Store.UpdateBooking(b)

	reply, err := ai.RescheduleReply(ctx, a.Groq, cust.Name, svc.Name, coalesce(cust.Language, "en"), newStart, newEnd)
	if err != nil {
		reply = "Rescheduled to " + newStart.Format("Mon 2 Jan, 3:04 PM") + "."
	}
	writeJSON(w, 200, map[string]any{"booking": updated, "message": reply})
}

func (a *App) aiPricingSuggestion(w http.ResponseWriter, r *http.Request) {
	if !a.Groq.IsConfigured() {
		writeErr(w, 503, "AI pricing suggestions require GROQ_API_KEY to be set on the server")
		return
	}
	serviceID := r.URL.Query().Get("service_id")
	svc, ok := a.Store.GetService(serviceID)
	if !ok {
		writeErr(w, 404, "service not found")
		return
	}
	days := 7
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			days = n
		}
	}
	fillPct := engine.FillRatePct(a.Store, svc.ProviderID, time.Now(), days)
	suggestion, err := ai.SuggestPricing(r.Context(), a.Groq, svc.Name, svc.BasePrice, svc.Currency, fillPct, days)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"service":       svc.Name,
		"fill_rate_pct": fillPct,
		"window_days":   days,
		"suggestion":    suggestion,
	})
}

// ---------- small utilities ----------

func coalesce(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// matchService fuzzy-matches an AI-extracted keyword (and optional provider
// name hint) against known services by simple case-insensitive substring
// matching. Deliberately simple and transparent — no AI involved here, so
// the final match is always auditable.
func matchService(services []models.Service, keyword, providerHint string, s *store.Store) (models.Service, bool) {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return models.Service{}, false
	}
	hint := strings.ToLower(strings.TrimSpace(providerHint))

	var best models.Service
	found := false
	for _, sv := range services {
		name := strings.ToLower(sv.Name)
		if strings.Contains(name, kw) || strings.Contains(kw, name) {
			if hint != "" {
				if p, ok := s.GetProvider(sv.ProviderID); ok && strings.Contains(strings.ToLower(p.Name), hint) {
					return sv, true // exact provider+service match wins immediately
				}
			}
			if !found {
				best, found = sv, true
			}
		}
	}
	return best, found
}

// resolveIntentTime turns a BookingIntent's date/time strings into a
// concrete time.Time, defaulting sensibly when fields are missing.
func resolveIntentTime(intent ai.BookingIntent, now time.Time) time.Time {
	loc := now.Location()
	date := intent.DateISO
	if date == "" {
		date = now.Format("2006-01-02")
	}
	timeStr := intent.TimeWindowStart
	if timeStr == "" {
		timeStr = "10:00"
	}
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+timeStr, loc)
	if err != nil {
		return engine.ClampToWorkingHours(now.Add(24 * time.Hour))
	}
	return t
}
