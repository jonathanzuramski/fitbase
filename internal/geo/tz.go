package geo

import (
	"sync"
	"time"

	"github.com/ringsaturn/tzf"
)

var (
	tzOnce   sync.Once
	tzFinder tzf.F
	tzErr    error
)

// ZoneName returns the IANA timezone name (e.g. "America/Denver") in effect
// at the coordinate, worldwide. ok is false over open ocean or if the finder
// failed to initialize.
func ZoneName(lat, lng float64) (name string, ok bool) {
	tzOnce.Do(func() { tzFinder, tzErr = tzf.NewDefaultFinder() })
	if tzErr != nil {
		return "", false
	}
	name = tzFinder.GetTimezoneName(lng, lat)
	return name, name != ""
}

// UTCOffsetAt returns the UTC offset in seconds that the coordinate's
// timezone observed at instant t — DST is resolved for that date, not today's.
// ok is false when the zone can't be determined or loaded.
func UTCOffsetAt(lat, lng float64, t time.Time) (secs int, ok bool) {
	name, ok := ZoneName(lat, lng)
	if !ok {
		return 0, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return 0, false
	}
	_, offset := t.In(loc).Zone()
	return offset, true
}
