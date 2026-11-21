package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fitbase/fitbase/internal/api"
	"github.com/fitbase/fitbase/internal/models"
)

func putFTP(h *api.Handler, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.SetFTP(rr, httptest.NewRequest(http.MethodPut, "/api/athlete/ftp", strings.NewReader(body)))
	return rr
}

func TestSetFTP_Validation(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, body := range []string{`{}`, `{"ftp_watts":0}`, `{"ftp_watts":601}`, `nope`} {
		if rr := putFTP(h, body); rr.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d, want 400", body, rr.Code)
		}
	}
}

// The endpoint exists so "use this FTP" buttons can't clobber the profile the
// way a partial PUT /api/athlete does.
func TestSetFTP_KeepsProfileAndReturnsCheck(t *testing.T) {
	h, d := newTestHandler(t)
	if err := d.UpdateAthlete(&models.Athlete{FTPWatts: 245, WeightKG: 80, ThresholdHR: 174, MaxHR: 192,
		RestingHR: 52, Timezone: "America/New_York", Units: "imperial"}); err != nil {
		t.Fatal(err)
	}
	rr := putFTP(h, `{"ftp_watts":215}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rr.Code, rr.Body.String())
	}
	var check models.FTPCheck
	decodeEnvelope(t, rr.Body.Bytes(), &check)
	if check.FTPWatts != 215 || check.Status != models.FTPCheckUnverified {
		t.Errorf("check = %+v, want FTP 215, unverified (no rides)", check)
	}
	a, _ := d.GetAthlete()
	if a.FTPWatts != 215 || a.MaxHR != 192 || a.RestingHR != 52 || a.Timezone != "America/New_York" || a.Units != "imperial" {
		t.Errorf("athlete after PUT /ftp = %+v", a)
	}
}

func TestDismissFTPCheck(t *testing.T) {
	h, d := newTestHandler(t)
	dismiss := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.DismissFTPCheck(rr, httptest.NewRequest(http.MethodPost, "/api/athlete/ftp-check/dismiss", nil))
		return rr
	}
	if rr := dismiss(); rr.Code != http.StatusConflict {
		t.Errorf("dismiss with no verdict: %d, want 409", rr.Code)
	}

	if err := d.SetFTP(245); err != nil {
		t.Fatal(err)
	}
	// insertSampleWorkout records the ride in 2024; move it into the 90-day window.
	insertSampleWorkout(t, d, "best")
	if _, err := d.Exec("UPDATE workouts SET recorded_at=? WHERE id='best'",
		time.Now().AddDate(0, 0, -5).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertPowerCurve("best", map[int]int{1200: 226}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.GetFTPCheck(rr, httptest.NewRequest(http.MethodGet, "/api/athlete/ftp-check", nil))
	var check models.FTPCheck
	decodeEnvelope(t, rr.Body.Bytes(), &check)
	if check.Status != models.FTPCheckLower || check.EstimateWatts != 215 || check.Dismissed {
		t.Fatalf("check = %+v, want lower 215 W, not dismissed", check)
	}

	if rr := dismiss(); rr.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", rr.Code, rr.Body.String())
	}
	if got, _ := d.FTPCheck(time.Now()); !got.Dismissed {
		t.Error("check should be dismissed after POST /dismiss")
	}
}
