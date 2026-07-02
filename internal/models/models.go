package models

import "time"

// Provider is anyone/anything that can be booked: a stylist, a consultant,
// a meeting room, a doctor, a photographer, a food stall slot, etc.
type Provider struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Bio       string    `json:"bio"`
	Tags      []string  `json:"tags"`     // e.g. ["haircut","color"]
	Outdoor   bool      `json:"outdoor"`  // true if service normally happens outdoors (enables weather advisory)
	Latitude  float64   `json:"latitude"` // used for weather lookups
	Longitude float64   `json:"longitude"`
	CreatedAt time.Time `json:"created_at"`
}

// Service is something a Provider offers, with a duration and base price.
type Service struct {
	ID          string  `json:"id"`
	ProviderID  string  `json:"provider_id"`
	Name        string  `json:"name"`
	DurationMin int     `json:"duration_min"`
	BasePrice   float64 `json:"base_price"`
	Currency    string  `json:"currency"`
}

// Customer represents the person booking.
type Customer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Contact  string `json:"contact"`  // email or phone
	Language string `json:"language"` // preferred language for AI messages, e.g. "bn", "en", "hi"
}

type BookingStatus string

const (
	StatusPending    BookingStatus = "pending"
	StatusConfirmed  BookingStatus = "confirmed"
	StatusCancelled  BookingStatus = "cancelled"
	StatusCompleted  BookingStatus = "completed"
	StatusWaitlisted BookingStatus = "waitlisted"
)

// Booking is a single reservation of a Service/Provider for a Customer at a time slot.
type Booking struct {
	ID           string        `json:"id"`
	ProviderID   string        `json:"provider_id"`
	ServiceID    string        `json:"service_id"`
	CustomerID   string        `json:"customer_id"`
	Start        time.Time     `json:"start"`
	End          time.Time     `json:"end"`
	Status       BookingStatus `json:"status"`
	Notes        string        `json:"notes"`        // free text customer notes / cancellation reasons
	Flexibility  string        `json:"flexibility"`  // free text, e.g. "any time next week works"
	RiskNote     string        `json:"risk_note"`    // AI-generated no-show risk assessment
	Confirmation string        `json:"confirmation"` // AI-generated confirmation message (in customer's language)
	WeatherNote  string        `json:"weather_note"` // AI-generated weather advisory (outdoor providers only)
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// ConflictProposal is generated when two customers want an overlapping slot.
// The AI mediator drafts an alternative for the customer who loses the slot.
type ConflictProposal struct {
	ID               string    `json:"id"`
	LosingBookingID  string    `json:"losing_booking_id"`
	WinningBookingID string    `json:"winning_booking_id"`
	Message          string    `json:"message"` // AI-drafted, personalized message to losing customer
	SuggestedStart   time.Time `json:"suggested_start"`
	SuggestedEnd     time.Time `json:"suggested_end"`
	Status           string    `json:"status"` // "pending", "accepted", "declined"
	CreatedAt        time.Time `json:"created_at"`
}
