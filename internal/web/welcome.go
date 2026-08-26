package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/fitbase/fitbase/internal/models"
)

// The first-run welcome flow: the profile form, its submit, and the skip
// escape hatch. The index handler redirects here until setup is complete.

func (th *templateHandler) welcomeGet(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, th.templates().welcome, "welcome", nil)
}

func (th *templateHandler) welcomePost(w http.ResponseWriter, r *http.Request) {
	units := r.FormValue("units")
	if units != "imperial" && units != "metric" {
		units = "imperial"
	}

	weightRaw, err := strconv.ParseFloat(r.FormValue("weight"), 64)
	if err != nil || weightRaw < 0 {
		weightRaw = 0
	}
	weightKG := weightRaw
	if units == "imperial" && weightKG > 0 {
		weightKG = weightRaw / 2.20462
	}

	tz := r.FormValue("timezone")
	if _, err := time.LoadLocation(tz); err != nil {
		tz = "UTC"
	}

	lang := r.FormValue("language")
	if lang == "" {
		lang = "en"
	}

	age, _ := strconv.Atoi(r.FormValue("age"))
	restingHR, _ := strconv.Atoi(r.FormValue("resting_hr"))
	thresholdHR, _ := strconv.Atoi(r.FormValue("threshold_hr"))
	maxHR, _ := strconv.Atoi(r.FormValue("max_hr"))
	ftpWatts, _ := strconv.Atoi(r.FormValue("ftp_watts"))
	if ftpWatts <= 0 {
		ftpWatts = 0 // leave at DB default (250) if not provided
	}

	a := &models.Athlete{
		Age: age, Location: r.FormValue("location"),
		Language: lang, Timezone: tz, Units: units,
		WeightKG: weightKG, RestingHR: restingHR,
		ThresholdHR: thresholdHR, MaxHR: maxHR, FTPWatts: ftpWatts,
	}
	if err := th.db.SaveWelcomeProfile(a); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (th *templateHandler) welcomeSkip(w http.ResponseWriter, r *http.Request) {
	if err := th.db.MarkSetupComplete(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
