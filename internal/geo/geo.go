// Package geo resolves a GPS coordinate to human place context — the US
// county/state it falls in, and the IANA timezone in effect there. Both
// lookups are fully offline: county boundaries are embedded US Census data
// and timezone shapes come from the tzf library, so imports never make a
// network call and results are reproducible forever.
package geo

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	_ "embed"
)

// US Census 2010 cartographic boundary counties, 20m resolution (public
// domain). ~900KB gzipped; lazily decoded on first lookup.
//
//go:embed data/us_counties.geojson.gz
var countiesGz []byte

// county is one county's display names plus its boundary, flattened to a
// ring set. Even-odd ray casting over all rings of a feature classifies both
// holes and disjoint MultiPolygon parts correctly, so polygon structure
// beyond the rings themselves doesn't need to be preserved.
type county struct {
	name           string // e.g. "Boulder County", "Orleans Parish"
	state          string // USPS code, e.g. "CO"
	rings          [][][2]float64
	minLat, maxLat float64
	minLng, maxLng float64
}

var (
	loadOnce sync.Once
	counties []county
	loadErr  error
)

// CountyState returns the county display name and two-letter state code
// containing the coordinate. ok is false outside the dataset (non-US rides,
// open water) or if the embedded data failed to load.
func CountyState(lat, lng float64) (countyName, state string, ok bool) {
	loadOnce.Do(loadCounties)
	if loadErr != nil {
		return "", "", false
	}
	for i := range counties {
		c := &counties[i]
		if lat < c.minLat || lat > c.maxLat || lng < c.minLng || lng > c.maxLng {
			continue
		}
		if evenOddContains(c.rings, lat, lng) {
			return c.name, c.state, true
		}
	}
	return "", "", false
}

type geoFeature struct {
	Properties struct {
		State string `json:"STATE"` // FIPS code
		Name  string `json:"NAME"`
		LSAD  string `json:"LSAD"` // legal/statistical area description: County, Parish, …
	} `json:"properties"`
	Geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

func loadCounties() {
	gz, err := gzip.NewReader(bytes.NewReader(countiesGz))
	if err != nil {
		loadErr = fmt.Errorf("geo: open embedded counties: %w", err)
		return
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		loadErr = fmt.Errorf("geo: decompress counties: %w", err)
		return
	}
	var fc struct {
		Features []geoFeature `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		loadErr = fmt.Errorf("geo: parse counties: %w", err)
		return
	}

	counties = make([]county, 0, len(fc.Features))
	for _, f := range fc.Features {
		state, known := stateByFIPS[f.Properties.State]
		if !known {
			continue
		}
		var rings [][][2]float64
		switch f.Geometry.Type {
		case "Polygon":
			var poly [][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &poly); err != nil {
				continue
			}
			rings = poly
		case "MultiPolygon":
			var mp [][][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &mp); err != nil {
				continue
			}
			for _, poly := range mp {
				rings = append(rings, poly...)
			}
		default:
			continue
		}
		if len(rings) == 0 {
			continue
		}

		name := f.Properties.Name
		if f.Properties.LSAD != "" {
			name += " " + f.Properties.LSAD
		}
		c := county{name: name, state: state, rings: rings}
		c.minLat, c.maxLat = 90, -90
		c.minLng, c.maxLng = 180, -180
		for _, ring := range rings {
			for _, pt := range ring {
				c.minLng = min(c.minLng, pt[0])
				c.maxLng = max(c.maxLng, pt[0])
				c.minLat = min(c.minLat, pt[1])
				c.maxLat = max(c.maxLat, pt[1])
			}
		}
		counties = append(counties, c)
	}
}

// evenOddContains reports whether (lat, lng) is inside the ring set by
// even-odd ray casting: a horizontal ray toward +lng crossing the boundary an
// odd number of times is inside. Holes and disjoint parts each add their own
// crossings, so no polygon bookkeeping is needed.
func evenOddContains(rings [][][2]float64, lat, lng float64) bool {
	inside := false
	for _, ring := range rings {
		n := len(ring)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			yi, yj := ring[i][1], ring[j][1]
			xi, xj := ring[i][0], ring[j][0]
			if (yi > lat) != (yj > lat) &&
				lng < (xj-xi)*(lat-yi)/(yj-yi)+xi {
				inside = !inside
			}
		}
	}
	return inside
}

// stateByFIPS maps Census state FIPS codes to USPS abbreviations.
var stateByFIPS = map[string]string{
	"01": "AL", "02": "AK", "04": "AZ", "05": "AR", "06": "CA", "08": "CO",
	"09": "CT", "10": "DE", "11": "DC", "12": "FL", "13": "GA", "15": "HI",
	"16": "ID", "17": "IL", "18": "IN", "19": "IA", "20": "KS", "21": "KY",
	"22": "LA", "23": "ME", "24": "MD", "25": "MA", "26": "MI", "27": "MN",
	"28": "MS", "29": "MO", "30": "MT", "31": "NE", "32": "NV", "33": "NH",
	"34": "NJ", "35": "NM", "36": "NY", "37": "NC", "38": "ND", "39": "OH",
	"40": "OK", "41": "OR", "42": "PA", "44": "RI", "45": "SC", "46": "SD",
	"47": "TN", "48": "TX", "49": "UT", "50": "VT", "51": "VA", "53": "WA",
	"54": "WV", "55": "WI", "56": "WY",
	"60": "AS", "66": "GU", "69": "MP", "72": "PR", "78": "VI",
}
