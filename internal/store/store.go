// Package store implements a small, dependency-free persistence layer.
// It keeps everything in memory for speed and periodically/immediately
// flushes to a single JSON file on disk, so the whole app runs with
// zero external database and zero third-party Go modules.
package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"

	"smartbook/internal/models"
)

type snapshot struct {
	Providers map[string]models.Provider         `json:"providers"`
	Services  map[string]models.Service          `json:"services"`
	Customers map[string]models.Customer         `json:"customers"`
	Bookings  map[string]models.Booking          `json:"bookings"`
	Conflicts map[string]models.ConflictProposal `json:"conflicts"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	data snapshot
}

func New(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: snapshot{
			Providers: map[string]models.Provider{},
			Services:  map[string]models.Service{},
			Customers: map[string]models.Customer{},
			Bookings:  map[string]models.Booking{},
			Conflicts: map[string]models.ConflictProposal{},
		},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, &s.data)
}

// persist must be called with s.mu already held (read or write lock is fine
// for the purposes of this simple app; we always call it under a write lock).
func (s *Store) persist() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func NewID(prefix string) string {
	return fmt.Sprintf("%s_%d_%04d", prefix, time.Now().UnixNano()/1e6, rand.Intn(10000))
}

// ---------- Providers ----------

func (s *Store) CreateProvider(p models.Provider) (models.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = NewID("prov")
	}
	p.CreatedAt = time.Now()
	s.data.Providers[p.ID] = p
	return p, s.persist()
}

func (s *Store) ListProviders() []models.Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Provider, 0, len(s.data.Providers))
	for _, p := range s.data.Providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) GetProvider(id string) (models.Provider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data.Providers[id]
	return p, ok
}

// ---------- Services ----------

func (s *Store) CreateService(sv models.Service) (models.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sv.ID == "" {
		sv.ID = NewID("svc")
	}
	s.data.Services[sv.ID] = sv
	return sv, s.persist()
}

func (s *Store) ListServices() []models.Service {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Service, 0, len(s.data.Services))
	for _, sv := range s.data.Services {
		out = append(out, sv)
	}
	return out
}

func (s *Store) ListServicesByProvider(providerID string) []models.Service {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []models.Service{}
	for _, sv := range s.data.Services {
		if sv.ProviderID == providerID {
			out = append(out, sv)
		}
	}
	return out
}

func (s *Store) GetService(id string) (models.Service, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sv, ok := s.data.Services[id]
	return sv, ok
}

// ---------- Customers ----------

func (s *Store) UpsertCustomer(c models.Customer) (models.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = NewID("cust")
	}
	if c.Language == "" {
		c.Language = "en"
	}
	s.data.Customers[c.ID] = c
	return c, s.persist()
}

func (s *Store) GetCustomer(id string) (models.Customer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.data.Customers[id]
	return c, ok
}

func (s *Store) ListCustomers() []models.Customer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Customer, 0, len(s.data.Customers))
	for _, c := range s.data.Customers {
		out = append(out, c)
	}
	return out
}

// ---------- Bookings ----------

func (s *Store) CreateBooking(b models.Booking) (models.Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ID == "" {
		b.ID = NewID("bkg")
	}
	now := time.Now()
	b.CreatedAt = now
	b.UpdatedAt = now
	s.data.Bookings[b.ID] = b
	return b, s.persist()
}

func (s *Store) UpdateBooking(b models.Booking) (models.Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b.UpdatedAt = time.Now()
	s.data.Bookings[b.ID] = b
	return b, s.persist()
}

func (s *Store) GetBooking(id string) (models.Booking, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.data.Bookings[id]
	return b, ok
}

func (s *Store) ListBookings() []models.Booking {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Booking, 0, len(s.data.Bookings))
	for _, b := range s.data.Bookings {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func (s *Store) ListBookingsByProvider(providerID string) []models.Booking {
	all := s.ListBookings()
	out := []models.Booking{}
	for _, b := range all {
		if b.ProviderID == providerID {
			out = append(out, b)
		}
	}
	return out
}

// ActiveBookingsOverlapping returns bookings for a provider that overlap the
// given [start,end) window and are not cancelled.
func (s *Store) ActiveBookingsOverlapping(providerID string, start, end time.Time) []models.Booking {
	out := []models.Booking{}
	for _, b := range s.ListBookingsByProvider(providerID) {
		if b.Status == models.StatusCancelled {
			continue
		}
		if b.Start.Before(end) && start.Before(b.End) {
			out = append(out, b)
		}
	}
	return out
}

// ---------- Conflict proposals ----------

func (s *Store) CreateConflict(c models.ConflictProposal) (models.ConflictProposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = NewID("conf")
	}
	c.CreatedAt = time.Now()
	s.data.Conflicts[c.ID] = c
	return c, s.persist()
}

func (s *Store) GetConflict(id string) (models.ConflictProposal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.data.Conflicts[id]
	return c, ok
}

func (s *Store) UpdateConflict(c models.ConflictProposal) (models.ConflictProposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Conflicts[c.ID] = c
	return c, s.persist()
}
