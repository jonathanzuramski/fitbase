package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/fitbase/fitbase/internal/models"
)

// FuncMap returns the custom template functions for all pages.
var FuncMap = template.FuncMap{
	"formatDuration":  formatDuration,
	"formatDistance":  formatDistance,
	"formatElevation": formatElevation,
	"formatSpeed":     formatSpeed,
	"formatOptFloat":  formatOptFloat,
	"formatOptInt":    formatOptInt,
	"formatIF":        formatIF,
	"hasGPS":          hasGPS,
	"toJSON":          toJSON,
	"lower":           strings.ToLower,
	"formatPace":      formatPace,
	"sortURL":         sortURL,
	"sortArrow":       sortArrow,
	"filterURL":       filterURL,
	"paginationURL":   paginationURL,
	"formatFloat1":    func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"not":             func(b bool) bool { return !b },
	"add1":            func(i int) int { return i + 1 },
}

// sortURL returns the href for a sortable column header, preserving the active type filter.
// Clicking an already-active column flips the direction; otherwise defaults to desc.
func sortURL(currentSort, currentDir, col, typeFilter string) string {
	dir := "desc"
	if currentSort == col && currentDir == "desc" {
		dir = "asc"
	}
	params := url.Values{"sort": {col}, "dir": {dir}}
	if typeFilter != "" {
		params.Set("type", typeFilter)
	}
	return "/?" + params.Encode()
}

// filterURL returns the href for a filter tab, preserving the current sort state.
func filterURL(sort, dir, typeFilter string) string {
	params := url.Values{}
	if typeFilter != "" {
		params.Set("type", typeFilter)
	}
	if sort != "" {
		params.Set("sort", sort)
		params.Set("dir", dir)
	}
	if len(params) == 0 {
		return "/"
	}
	return "/?" + params.Encode()
}

// paginationURL builds a pagination href, preserving sort and type filter state.
func paginationURL(page int, sort, dir, typeFilter string) string {
	params := url.Values{"page": {strconv.Itoa(page)}}
	if sort != "" {
		params.Set("sort", sort)
		params.Set("dir", dir)
	}
	if typeFilter != "" {
		params.Set("type", typeFilter)
	}
	return "/?" + params.Encode()
}

// sortArrow returns the indicator character for a column header, or empty string.
func sortArrow(currentSort, currentDir, col string) template.HTML {
	if currentSort != col {
		return ""
	}
	if currentDir == "asc" {
		return "↑"
	}
	return "↓"
}

func formatDuration(secs int) string {
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func formatDistance(meters float64, imperial bool) string {
	if imperial {
		miles := meters / 1609.344
		if miles >= 100 {
			return fmt.Sprintf("%.0fmi", miles)
		}
		return fmt.Sprintf("%.1fmi", miles)
	}
	km := meters / 1000.0
	if km >= 100 {
		return fmt.Sprintf("%.0fkm", km)
	}
	return fmt.Sprintf("%.1fkm", km)
}

func formatElevation(meters float64, imperial bool) string {
	if imperial {
		return fmt.Sprintf("%.0fft", math.Round(meters*3.28084))
	}
	return fmt.Sprintf("%.0f m", math.Round(meters))
}

func formatSpeed(mps float64, imperial bool) string {
	if imperial {
		return fmt.Sprintf("%.1fmph", mps*2.23694)
	}
	return fmt.Sprintf("%.1fkm/h", mps*3.6)
}

func formatOptFloat(v *float64, unit string) string {
	if v == nil {
		return "—"
	}
	if unit == "" {
		return fmt.Sprintf("%.0f", *v)
	}
	return fmt.Sprintf("%.0f %s", *v, unit)
}

func formatOptInt(v *int, unit string) string {
	if v == nil {
		return "—"
	}
	if unit == "" {
		return fmt.Sprintf("%d", *v)
	}
	return fmt.Sprintf("%d %s", *v, unit)
}

func formatPace(mps float64, imperial bool) string {
	if mps <= 0 {
		return "—"
	}
	var secsPerUnit float64
	var unit string
	if imperial {
		secsPerUnit = 1609.344 / mps
		unit = "/mi"
	} else {
		secsPerUnit = 1000.0 / mps
		unit = "/km"
	}
	mins := int(secsPerUnit) / 60
	secs := int(secsPerUnit) % 60
	return fmt.Sprintf("%d:%02d%s", mins, secs, unit)
}

func formatIF(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *v)
}

func hasGPS(streams []models.Stream) bool {
	for _, s := range streams {
		if s.Lat != nil && s.Lng != nil {
			return true
		}
	}
	return false
}

func toJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(b)
}
