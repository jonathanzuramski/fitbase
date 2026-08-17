package timeutil

import (
	"net/http"
	"net/url"
	"time"
)

// TZCookieName is the cookie the base template sets on every page load with
// the browser's IANA timezone (Intl.DateTimeFormat().resolvedOptions().timeZone).
const TZCookieName = "tz"

// ViewerLocation resolves the timezone of whoever is looking at the app right
// now: the tz cookie when present and valid, otherwise fallback (the athlete's
// profile timezone). The cookie is client input, so time.LoadLocation doubles
// as its validator — anything it rejects falls through to the fallback.
func ViewerLocation(r *http.Request, fallback *time.Location) *time.Location {
	if r != nil {
		if c, err := r.Cookie(TZCookieName); err == nil {
			if name, err := url.QueryUnescape(c.Value); err == nil {
				if loc, err := time.LoadLocation(name); err == nil {
					return loc
				}
			}
		}
	}
	return fallback
}
