# SmartBook — an AI-native booking engine in Go

SmartBook is a booking/scheduling backend (+ a small demo UI) for anything
that gets booked in time slots — salons, consultants, meeting rooms, tutors,
photographers, clinics. It's written in pure Go standard library (no
third-party packages — `go build` just works, no `go mod download`, no
Docker, no database server) and uses **Groq's free-tier LLM API** to layer
genuine AI behavior on top of a deterministic, always-correct scheduling
core.

The honest pitch isn't "no one has ever built a booking app before" — it's
that most booking apps bolt a chatbot onto a CRUD form. SmartBook instead
uses the LLM only where language understanding actually adds value, and
keeps every scheduling decision (is this slot free? is this slot next
available?) in plain, auditable Go code that never depends on the AI being
available, correct, or even configured. Take the specific combination below
— natural-language booking + AI-mediated double-booking resolution +
weather-aware outdoor advisories + multilingual confirmations, all running
on a single dependency-free Go binary against Groq's free tier — as the
"new" part.

---

## What makes it different

1. **Book in plain English.** `POST /api/ai/book` with
   `"book me a haircut tomorrow around 3pm, any stylist is fine"` and the
   app extracts service/date/time/flexibility with an LLM, matches it to a
   real service, finds a slot, and books it — or asks a clarifying question
   if it's genuinely ambiguous.

2. **AI-mediated conflict resolution.** This is the feature I haven't seen
   in typical booking software: when two people want the same slot,
   instead of a blunt "sorry, taken" error, the loser gets waitlisted onto
   the nearest free alternative and the LLM drafts a short, personalized,
   empathetic message offering that time — which the customer can accept
   or decline via API (or the demo UI). The *slot-finding itself* is 100%
   deterministic Go code; the LLM only writes the human-facing message.

3. **Weather-aware advisories for outdoor bookings.** Providers can be
   tagged `outdoor: true`. On booking, SmartBook calls
   [Open-Meteo](https://open-meteo.com) (a genuinely free, no-API-key
   weather API) for the slot's date/location, and if rain/storms are
   likely, an LLM drafts a friendly heads-up suggesting the customer
   confirm or reschedule. No booking app that isn't purpose-built for
   outdoor events typically does this.

4. **Multilingual, personalized confirmations & reschedules.** Every
   customer has a `language` preference (English, Bengali, Hindi, or
   anything else you tell it) and confirmation/reschedule messages are
   generated in that language/script by the LLM — not a canned template
   with `{{name}}` substitution.

5. **No-show risk notes.** The LLM reads a customer's free-text booking
   history/notes and returns a one-line plain-English risk read
   ("Medium — mentioned running late twice, consider a reminder text").
   This is explicitly an LLM judgment call, not a trained ML model —
   the README says so because that distinction matters.

6. **AI pricing advisor.** Given how full a provider's schedule already is
   for an upcoming window, the LLM suggests (never auto-applies) a price
   adjustment with reasoning. Decision support, not a black-box auto-charge.

7. **Fails soft, always.** Every AI call has a deterministic fallback. If
   `GROQ_API_KEY` isn't set, or Groq's free-tier rate limit is hit, or the
   network is down, the *booking system itself keeps working* — you just
   lose the AI-generated flourishes (a template confirmation is used
   instead of an LLM one, etc). AI-only endpoints (`/api/ai/book`,
   `/api/ai/reschedule`, `/api/ai/pricing-suggestion`) return a clean `503`
   with an explanation rather than a stack trace.

---

## Architecture

```
main.go                    — wiring, env/.env loading, HTTP server, embeds static/
internal/models/           — plain structs: Provider, Service, Customer, Booking, ConflictProposal
internal/store/            — JSON-file backed persistence (thread-safe, zero dependencies)
internal/engine/           — deterministic scheduling: working hours, overlap checks, slot search
internal/groq/              — minimal Groq (OpenAI-compatible) chat-completions client
internal/weather/          — Open-Meteo client (free, no API key)
internal/ai/                — every prompt used in the app, one file, easy to tune
internal/handlers/         — HTTP handlers that combine engine + ai + weather
static/index.html          — single-file demo UI (vanilla JS, no CDN dependency)
data/smartbook.json        — created at runtime; your entire dataset, human-readable
```

**Why JSON-file storage instead of Postgres/SQLite?** Two reasons. First,
this sandbox environment can't reach the Go module proxy, so any package
requiring `go mod download` (including SQLite drivers) won't build here —
sticking to the standard library was a hard constraint, not just a style
choice. Second, it's genuinely a fine choice for the target scale (a single
small business's booking calendar): the whole dataset is one readable,
diffable, backup-able JSON file, and the store is trivially swappable —
`internal/store/store.go` is one file with a clean method set, so swapping
in Postgres later is a contained, mechanical change that doesn't touch the
engine, AI layer, or handlers at all.

**Why Go 1.22, specifically?** `net/http`'s `ServeMux` gained method-aware
routing (`mux.HandleFunc("POST /api/bookings", ...)`) and `r.PathValue()`
in 1.22, which is enough of a router for an app this size — no need for a
third-party router package either.

---

## AI features and which Groq model each uses

| Feature | Endpoint | Model | Why this model |
|---|---|---|---|
| Natural-language intent parsing | `POST /api/ai/book`, `/api/ai/reschedule` | `llama-3.3-70b-versatile` | Structured JSON extraction benefits from the stronger model; free-tier budget for it is smaller (~1,000 req/day) but this is a low-frequency, high-value call. |
| Confirmation messages | (automatic on booking) | `llama-3.1-8b-instant` | Short, low-stakes text generation; this model has the largest free-tier budget (~14,400 req/day), so it's used for anything called on every booking. |
| Conflict mediation message | (automatic on double-booking) | `llama-3.1-8b-instant` | Same reasoning — frequent, short, low-stakes. |
| No-show risk note | (automatic on booking) | `llama-3.1-8b-instant` | Same. |
| Weather advisory | (automatic on outdoor booking) | `llama-3.1-8b-instant` | Same. |
| Reschedule reply | `POST /api/ai/reschedule` | `llama-3.1-8b-instant` | Same. |
| Pricing suggestion | `GET /api/ai/pricing-suggestion` | `llama-3.1-8b-instant` | Same. |

All of this lives in `internal/groq/groq.go` (the client) and
`internal/ai/ai.go` (the prompts) — change model names or prompts in one
place.

Groq's free tier (as of mid-2026) requires no credit card and gives you
roughly 30 requests/minute and up to ~14,400 requests/day depending on the
model — plenty for development, demos, and small real-world usage. Exact
numbers change over time; check
[console.groq.com](https://console.groq.com/docs/rate-limits) for current
limits before relying on this for production traffic.

---

## Setup guide, from scratch

### 1. Install Go

You need Go 1.22 or newer.

- **Ubuntu/Debian:** `sudo apt-get update && sudo apt-get install golang-go`
- **macOS (Homebrew):** `brew install go`
- **Windows:** download the installer from [go.dev/dl](https://go.dev/dl/)
- Verify with: `go version` (should print `go1.22` or higher)

### 2. Get the code

If you received this as a folder, just `cd` into it. If it's in a git repo:

```bash
git clone <your-repo-url> smartbook
cd smartbook
```

### 3. Get a free Groq API key

1. Go to [console.groq.com](https://console.groq.com) and sign up (email,
   Google, or GitHub — no credit card needed).
2. Open **API Keys** in the left sidebar → **Create API Key**.
3. Copy the key (starts with `gsk_...`).

### 4. Configure environment variables

```bash
cp .env.example .env
```

Open `.env` and paste your key:

```
GROQ_API_KEY=gsk_your_key_here
```

That's the only required variable. Everything else has a sensible default
(see `.env.example` for the full list — port, data file location, default
coordinates for weather lookups, etc).

> The app works fine with **no** Groq key too — it just runs in
> "core booking only" mode and every AI endpoint returns a clear 503
> instead of AI output. This is genuinely useful for testing the
> deterministic scheduling logic in isolation.

### 5. Run it

```bash
go run .
```

You should see:

```
Groq AI: configured ✅ (AI booking, mediation, pricing, weather advisories enabled)
SmartBook listening on http://localhost:8080
```

Open **http://localhost:8080** in a browser for the demo UI, or use the API
directly (examples below).

### 6. Build a standalone binary (optional)

```bash
go build -o smartbook .
./smartbook
```

This produces a single self-contained executable (the demo UI is embedded
via `go:embed`) — copy it anywhere, no other files needed except `.env` and
a writable `data/` directory next to it.

---

## Using the demo UI

1. Open `http://localhost:8080`.
2. Under **Quick setup**, create a provider (e.g. "Studio Luna") and a
   service under it (e.g. "Haircut", 30 min, ₹500).
3. In the chat box, type something like:
   `Book me a haircut tomorrow around 3pm, any stylist is fine`
4. Watch the AI parse it, book it, and generate a confirmation message —
   all visible in **Bookings** on the right.
5. Try booking the *same* slot again with a different name to see the
   conflict-mediation flow trigger.

---

## API reference

All bodies are JSON. Base URL: `http://localhost:8080`.

### Providers

```bash
curl -X POST localhost:8080/api/providers \
  -d '{"name":"Studio Luna","tags":["haircut","color"],"outdoor":false}'

curl localhost:8080/api/providers
```

### Services

```bash
curl -X POST localhost:8080/api/services \
  -d '{"provider_id":"prov_...","name":"Haircut","duration_min":30,"base_price":500,"currency":"INR"}'

curl "localhost:8080/api/services?provider_id=prov_..."
```

### Customers

```bash
curl -X POST localhost:8080/api/customers \
  -d '{"name":"Sayantan Roy","contact":"sayantan@example.com","language":"bn"}'
```

### Structured booking (no AI required)

```bash
curl -X POST localhost:8080/api/bookings \
  -d '{"service_id":"svc_...","customer_id":"cust_...","start":"2026-07-05T15:00:00+05:30","notes":"first visit"}'
```

Response includes either a confirmed `booking`, or a `booking` (waitlisted)
+ `conflict` object if the slot was taken.

```bash
curl -X POST localhost:8080/api/conflicts/conf_.../accept
curl -X POST localhost:8080/api/conflicts/conf_.../decline
curl -X POST localhost:8080/api/bookings/bkg_.../cancel -d '{"reason":"changed plans"}'
```

### Natural-language booking (requires `GROQ_API_KEY`)

```bash
curl -X POST localhost:8080/api/ai/book \
  -d '{"customer_name":"Sayantan","customer_contact":"sayantan@example.com","language":"bn","text":"Book me a haircut tomorrow around 3pm, any stylist is fine"}'
```

### Natural-language reschedule (requires `GROQ_API_KEY`)

```bash
curl -X POST localhost:8080/api/ai/reschedule \
  -d '{"booking_id":"bkg_...","text":"Something came up, can we move to next week same time?"}'
```

### AI pricing suggestion (requires `GROQ_API_KEY`)

```bash
curl "localhost:8080/api/ai/pricing-suggestion?service_id=svc_...&days=7"
```

### Health check

```bash
curl localhost:8080/health
# {"status":"ok","time":"...","groq_configured":true}
```

---

## Design decisions worth knowing about

- **Working hours are fixed at 09:00–18:00** in `internal/engine/engine.go`
  (`WorkStartHour` / `WorkEndHour`). Per-provider working hours are an
  obvious next step (see Roadmap).
- **Slot search** looks forward in 15-minute increments for up to N days
  (7 for new bookings, 14 for reschedules). This is deliberately simple and
  correct rather than clever — a priority queue over free intervals would
  be faster but this app's scale doesn't need it.
- **Every JSON write is atomic**: the store writes to a `.tmp` file and
  renames it over the real file, so a crash mid-write can't corrupt your
  data.
- **The AI never decides whether a slot is free.** It only ever (a) turns
  free text into structured fields, or (b) turns structured facts into
  human-readable text. This is intentional: correctness of the booking
  system should never depend on an LLM being right.

## Limitations & honest caveats

- No payment processing — the "pricing advisor" is advisory text, not a
  billing integration.
- No authentication/authorization layer — this is a backend + demo UI, not
  a production-hardened multi-tenant SaaS. Add auth before exposing this
  publicly.
- No-show risk assessment is an LLM's read of free-text notes, not a
  statistically validated model — treat it as a nudge, not a verdict.
- Groq's free tier is rate-limited; under real concurrent load you'll want
  the paid Developer tier or a fallback provider (Gemini/Cerebras/
  OpenRouter free tiers are reasonable pairings — see `internal/groq/`,
  which is intentionally small so swapping/adding a provider is easy).

## Roadmap ideas

- Per-provider working hours & holidays.
- SMS/WhatsApp delivery of AI-generated confirmations (Twilio free trial
  credit or WhatsApp Cloud API free tier).
- Voice booking via Groq's Whisper endpoint (also free-tier, already used
  by nothing here yet — `POST /api/ai/book-by-voice` would be a natural
  extension).
- Swap `internal/store` for Postgres/SQLite when you need multi-instance
  deployment; the interface is already small and clean.

---

## License

Use it, fork it, ship it — no restrictions implied by this README. Add a
LICENSE file with your preferred terms before distributing.
