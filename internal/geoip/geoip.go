// Package geoip resolves public IP addresses to coarse city-level locations
// via an external HTTP lookup (default ipwho.is; KERN_GEOIP_URL swaps or
// disables it). Lookups are best-effort hints for the fleet map — a failure
// leaves a runner without a location, never blocks registration or heartbeats.
package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Geo is a coarse location. Lat/Lng are WGS84 degrees.
type Geo struct {
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	City    string  `json:"city"`
	Country string  `json:"country"`
}

// Resolver performs lookups against an ipapi.co-shaped JSON endpoint.
type Resolver struct {
	url  string
	http *http.Client
}

// New returns a Resolver for the URL template ("{ip}" is replaced by the
// address; an empty address queries the caller's own egress). "off", "none"
// or an empty template returns nil — callers must nil-check.
func New(urlTemplate string) *Resolver {
	urlTemplate = strings.TrimSpace(urlTemplate)
	if urlTemplate == "" || urlTemplate == "off" || urlTemplate == "none" {
		return nil
	}
	return &Resolver{url: urlTemplate, http: &http.Client{Timeout: 8 * time.Second}}
}

// IsPublic reports whether ip is a routable public address worth resolving.
func IsPublic(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return a.IsGlobalUnicast() && !a.IsPrivate()
}

// Lookup resolves ip; an empty ip resolves the caller's own egress address.
func (r *Resolver) Lookup(ctx context.Context, ip string) (Geo, error) {
	u := r.url
	if ip == "" {
		u = strings.Replace(u, "{ip}/", "", 1)
	}
	u = strings.ReplaceAll(u, "{ip}", ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Geo{}, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return Geo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Geo{}, fmt.Errorf("geoip lookup: status %d", resp.StatusCode)
	}
	// Tolerant decode covering ipwho.is (success/country) and ipapi.co
	// (error/country_name); both name lat/lng the same way.
	var payload struct {
		Success     *bool   `json:"success"`
		Error       bool    `json:"error"`
		Reason      string  `json:"reason"`
		Message     string  `json:"message"`
		Lat         float64 `json:"latitude"`
		Lng         float64 `json:"longitude"`
		City        string  `json:"city"`
		Country     string  `json:"country"`
		CountryName string  `json:"country_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Geo{}, err
	}
	if payload.Error || (payload.Success != nil && !*payload.Success) {
		why := payload.Reason
		if why == "" {
			why = payload.Message
		}
		return Geo{}, fmt.Errorf("geoip lookup: %s", why)
	}
	if payload.Lat == 0 && payload.Lng == 0 {
		return Geo{}, fmt.Errorf("geoip lookup: no location for %s", ip)
	}
	country := payload.CountryName
	if country == "" {
		country = payload.Country
	}
	return Geo{Lat: payload.Lat, Lng: payload.Lng, City: payload.City, Country: country}, nil
}
