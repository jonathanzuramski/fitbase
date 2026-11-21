package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/timeutil"
)

// The Analytics endpoints serve the page's period comparison, trends and
// year-over-year charts, so changing a period or range doesn't reload the
// page; agents get the same numbers. "Today" is the viewer's (the tz cookie,
// else the athlete's timezone), and days are training days.

// GET /api/analytics/compare — one period against another
// (timeutil.ParseComparison for the parameters, plus scope).
func (h *Handler) GetAnalyticsCompare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := timeutil.ParseComparison(q, time.Now().In(h.viewerTZ(r)))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.db.ComparePeriods(c, q.Get("scope"))
	if err != nil {
		writeAnalyticsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/analytics/trends?range=1y — every marker over a range.
func (h *Handler) GetAnalyticsTrends(w http.ResponseWriter, r *http.Request) {
	out, err := h.db.AnalyticsTrends(r.URL.Query().Get("range"), time.Now().In(h.viewerTZ(r)))
	if err != nil {
		writeAnalyticsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/analytics/years?scope=cycling — the last five years, day by day.
func (h *Handler) GetAnalyticsYears(w http.ResponseWriter, r *http.Request) {
	out, err := h.db.YearOverYear(r.URL.Query().Get("scope"), time.Now().In(h.viewerTZ(r)))
	if err != nil {
		writeAnalyticsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeAnalyticsError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrInvalidParam) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "database error")
}
