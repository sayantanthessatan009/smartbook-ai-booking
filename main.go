// Command smartbook runs the SmartBook AI-native booking server.
// It is intentionally dependency-free (Go standard library only) so it
// builds and runs anywhere with just `go run .` — no `go mod download`,
// no database server, no Docker required.
package main

import (
	"bufio"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"smartbook/internal/groq"
	"smartbook/internal/handlers"
	"smartbook/internal/store"
	"smartbook/internal/weather"
)

//go:embed static
var staticFiles embed.FS

// loadDotEnv reads a simple KEY=VALUE .env file (if present) and applies any
// values that aren't already set in the real environment. No third-party
// dependency required for this — it's about a dozen lines.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // no .env file — that's fine, rely on real env vars
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func main() {
	loadDotEnv(".env")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	dataFile := os.Getenv("DATA_FILE")
	if dataFile == "" {
		dataFile = "data/smartbook.json"
	}
	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("could not create data directory: %v", err)
	}

	st, err := store.New(dataFile)
	if err != nil {
		log.Fatalf("could not open data store: %v", err)
	}

	app := &handlers.App{
		Store:   st,
		Groq:    groq.NewFromEnv(),
		Weather: weather.New(),
		// Defaults to Kolkata, India; override with DEFAULT_LAT / DEFAULT_LON.
		DefaultLat: envFloat("DEFAULT_LAT", 22.5726),
		DefaultLon: envFloat("DEFAULT_LON", 88.3639),
	}

	mux := handlers.NewMux(app)

	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("static assets error: %v", err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(staticSub)))

	if app.Groq.IsConfigured() {
		log.Println("Groq AI: configured ✅ (AI booking, mediation, pricing, weather advisories enabled)")
	} else {
		log.Println("Groq AI: NOT configured ⚠️  (set GROQ_API_KEY to enable AI features — core booking still works)")
	}

	addr := ":" + port
	log.Printf("SmartBook listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequests(mux)))
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
