package timeutil_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/timeutil"
)

func TestViewerLocation(t *testing.T) {
	fallback := time.UTC

	cases := []struct {
		name   string
		cookie string // "" = no cookie sent
		want   string // location name we expect back
	}{
		{"valid zone", "America/Denver", "America/Denver"},
		{"url-encoded zone", "America%2FDenver", "America/Denver"},
		{"no cookie", "", "UTC"},
		{"garbage zone", "Not/AZone", "UTC"},
		{"empty value", "%", "UTC"}, // also exercises the unescape-failure path
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tc.cookie != "" {
				r.Header.Set("Cookie", timeutil.TZCookieName+"="+tc.cookie)
			}
			if got := timeutil.ViewerLocation(r, fallback); got.String() != tc.want {
				t.Errorf("ViewerLocation(cookie=%q) = %q, want %q", tc.cookie, got, tc.want)
			}
		})
	}

	// nil request (background jobs) must fall through to the fallback.
	if got := timeutil.ViewerLocation(nil, fallback); got != fallback {
		t.Errorf("ViewerLocation(nil) = %q, want fallback", got)
	}
}
