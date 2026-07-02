// Package weather calls Open-Meteo (https://open-meteo.com), a genuinely
// free, no-API-key weather API, to power the "weather-aware booking
// advisory" feature: outdoor-tagged services get warned + offered a
// reschedule if rain/storms are forecast for their slot.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 10 * time.Second}}
}

type DayForecast struct {
	Date              string  `json:"date"`
	MaxTempC          float64 `json:"max_temp_c"`
	MinTempC          float64 `json:"min_temp_c"`
	PrecipitationMM   float64 `json:"precipitation_mm"`
	PrecipProbability int     `json:"precip_probability_pct"`
	WeatherCode       int     `json:"weather_code"`
	Summary           string  `json:"summary"`
}

// ForecastForDate returns the forecast for a specific date (YYYY-MM-DD) at a
// lat/lon. Open-Meteo's free daily-forecast endpoint needs no API key.
func (c *Client) ForecastForDate(ctx context.Context, lat, lon float64, date time.Time) (*DayForecast, error) {
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&daily=weathercode,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max&timezone=auto&start_date=%s&end_date=%s",
		lat, lon, date.Format("2006-01-02"), date.Format("2006-01-02"),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Daily struct {
			Time                     []string  `json:"time"`
			WeatherCode              []int     `json:"weathercode"`
			TempMax                  []float64 `json:"temperature_2m_max"`
			TempMin                  []float64 `json:"temperature_2m_min"`
			PrecipitationSum         []float64 `json:"precipitation_sum"`
			PrecipitationProbability []int     `json:"precipitation_probability_max"`
		} `json:"daily"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Daily.Time) == 0 {
		return nil, fmt.Errorf("weather: no forecast data returned")
	}
	return &DayForecast{
		Date:              out.Daily.Time[0],
		MaxTempC:          firstOr(out.Daily.TempMax),
		MinTempC:          firstOr(out.Daily.TempMin),
		PrecipitationMM:   firstOr(out.Daily.PrecipitationSum),
		PrecipProbability: firstIntOr(out.Daily.PrecipitationProbability),
		WeatherCode:       firstIntOr(out.Daily.WeatherCode),
		Summary:           describeCode(firstIntOr(out.Daily.WeatherCode)),
	}, nil
}

func firstOr(f []float64) float64 {
	if len(f) == 0 {
		return 0
	}
	return f[0]
}

func firstIntOr(f []int) int {
	if len(f) == 0 {
		return 0
	}
	return f[0]
}

// describeCode maps WMO weather codes (used by Open-Meteo) to a short label.
func describeCode(code int) string {
	switch {
	case code == 0:
		return "clear sky"
	case code <= 3:
		return "partly cloudy"
	case code <= 48:
		return "fog"
	case code <= 57:
		return "drizzle"
	case code <= 67:
		return "rain"
	case code <= 77:
		return "snow"
	case code <= 82:
		return "rain showers"
	case code <= 86:
		return "snow showers"
	case code <= 99:
		return "thunderstorm"
	default:
		return "unknown"
	}
}

// IsBadForOutdoor is a simple, transparent heuristic (not AI) for whether a
// forecast should trigger a weather advisory for an outdoor booking.
func (d DayForecast) IsBadForOutdoor() bool {
	return d.PrecipProbability >= 50 || d.PrecipitationMM >= 4 || d.WeatherCode >= 95
}
