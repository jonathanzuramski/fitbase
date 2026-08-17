package geo_test

import (
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/geo"
)

func TestCountyState(t *testing.T) {
	cases := []struct {
		name       string
		lat, lng   float64
		wantCounty string
		wantState  string
		wantOK     bool
	}{
		{"Boulder CO", 40.0150, -105.2705, "Boulder County", "CO", true},
		{"New Orleans LA", 29.9511, -90.0715, "Orleans Parish", "LA", true},
		{"Manhattan NY", 40.7831, -73.9712, "New York County", "NY", true},
		{"Paris France (outside dataset)", 48.8566, 2.3522, "", "", false},
		{"Atlantic ocean", 0, -30, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			county, state, ok := geo.CountyState(tc.lat, tc.lng)
			if ok != tc.wantOK || county != tc.wantCounty || state != tc.wantState {
				t.Errorf("CountyState(%v, %v) = %q, %q, %v; want %q, %q, %v",
					tc.lat, tc.lng, county, state, ok, tc.wantCounty, tc.wantState, tc.wantOK)
			}
		})
	}
}

func TestZoneName(t *testing.T) {
	name, ok := geo.ZoneName(40.0150, -105.2705)
	if !ok || name != "America/Denver" {
		t.Errorf("ZoneName(Boulder) = %q, %v; want America/Denver, true", name, ok)
	}
	name, ok = geo.ZoneName(48.8566, 2.3522)
	if !ok || name != "Europe/Paris" {
		t.Errorf("ZoneName(Paris) = %q, %v; want Europe/Paris, true", name, ok)
	}
}

// UTCOffsetAt must resolve DST for the ride's date, not today's rules.
func TestUTCOffsetAt_DST(t *testing.T) {
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	if secs, ok := geo.UTCOffsetAt(40.0150, -105.2705, winter); !ok || secs != -7*3600 {
		t.Errorf("Boulder winter offset = %d, %v; want %d", secs, ok, -7*3600)
	}
	if secs, ok := geo.UTCOffsetAt(40.0150, -105.2705, summer); !ok || secs != -6*3600 {
		t.Errorf("Boulder summer offset = %d, %v; want %d", secs, ok, -6*3600)
	}
}
