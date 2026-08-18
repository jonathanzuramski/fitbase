package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/fitbase/fitbase/internal/aicoach"
	"github.com/fitbase/fitbase/internal/db"
	"github.com/fitbase/fitbase/internal/dropbox"
	"github.com/fitbase/fitbase/internal/fitness"
	"github.com/fitbase/fitbase/internal/intervals"
	"github.com/fitbase/fitbase/internal/models"
)

// settingsView is the template data for the settings page.
type settingsView struct {
	Athlete       *models.Athlete
	WeightDisplay float64
	Imperial      bool
	PowerZones    []models.PowerZone
	HRZones       []models.HRZone
	HRZonesCustom bool
	FTPHistory    []db.FTPHistoryEntry
	Today         string

	DropboxConnected   bool
	DropboxFolder      string
	DropboxLongpoll    bool
	IntervalsConnected bool
	IntervalsAthleteID string
	IntervalsAutoSync  bool
	GDriveConfigured   bool
	GDriveConnected    bool
	GDriveClientID     string

	AIProvider   string
	AIModel      string
	AIConfigured bool
	AIProviders  []aicoach.ProviderInfo
}

func (th *templateHandler) settings(w http.ResponseWriter, r *http.Request) {
	athlete, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	imperial := th.isImperial()
	weightDisplay := athlete.WeightKG
	if imperial {
		weightDisplay = athlete.WeightKG * 2.20462
	}

	dropboxToken, _ := th.db.GetIntegrationToken("dropbox")
	dropboxConnected := dropboxToken != ""
	dropboxFolder, _, _ := th.db.GetIntegrationCredentials("dropbox")
	dropboxLongpoll, _ := th.db.GetAutoSync("dropbox")

	intervalsAthleteID, _, _ := th.db.GetIntegrationCredentials("intervals")
	intervalsConnected := intervalsAthleteID != ""
	intervalsAutoSync, _ := th.db.GetAutoSync("intervals")

	gdriveClientID, _, _ := th.db.GetIntegrationCredentials("gdrive")
	gdriveConfigured := gdriveClientID != ""

	ftpHistory, _ := th.db.AllFTPHistory()
	gdriveConnected := false
	if gdriveConfigured {
		if token, err := th.db.GetIntegrationToken("gdrive"); err == nil {
			gdriveConnected = token != ""
		}
	}

	aiSettings, _ := th.db.GetAISettings()

	renderTemplate(w, th.templates().settings, "base", settingsView{
		Athlete:       athlete,
		WeightDisplay: weightDisplay,
		Imperial:      imperial,
		PowerZones:    fitness.PowerZones(athlete.FTPWatts),
		HRZones:       resolveHRZones(athlete),
		HRZonesCustom: athlete.HRZonesJSON != "",
		FTPHistory:    ftpHistory,
		Today:         time.Now().Format("2006-01-02"),

		DropboxConnected:   dropboxConnected,
		DropboxFolder:      dropboxFolder,
		DropboxLongpoll:    dropboxLongpoll,
		IntervalsConnected: intervalsConnected,
		IntervalsAthleteID: intervalsAthleteID,
		IntervalsAutoSync:  intervalsAutoSync,
		GDriveConfigured:   gdriveConfigured,
		GDriveConnected:    gdriveConnected,
		GDriveClientID:     gdriveClientID,

		AIProvider:   aiSettings.Provider,
		AIModel:      aiSettings.Model,
		AIConfigured: aiSettings.Provider != "" && aiSettings.APIKey != "",
		AIProviders:  aicoach.AllInfo(),
	})
}

func (th *templateHandler) setUnits(w http.ResponseWriter, r *http.Request) {
	units := r.FormValue("units")
	if units != "imperial" && units != "metric" {
		units = "imperial"
	}
	if err := th.db.UpdateAthleteUnits(units); err != nil {
		slog.Error("update units", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	ref := r.Header.Get("Referer")
	if ref == "" {
		ref = "/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}

func (th *templateHandler) updateAthlete(w http.ResponseWriter, r *http.Request) {
	ftp, err := strconv.Atoi(r.FormValue("ftp_watts"))
	if err != nil || ftp <= 0 {
		http.Error(w, "invalid ftp_watts", http.StatusBadRequest)
		return
	}
	weightRaw, err := strconv.ParseFloat(r.FormValue("weight"), 64)
	if err != nil || weightRaw <= 0 {
		http.Error(w, "invalid weight", http.StatusBadRequest)
		return
	}
	weightKG := weightRaw
	if th.isImperial() {
		weightKG = weightRaw / 2.20462
	}
	thresholdHR, _ := strconv.Atoi(r.FormValue("threshold_hr"))
	maxHR, _ := strconv.Atoi(r.FormValue("max_hr"))
	if thresholdHR < 0 {
		thresholdHR = 0
	}
	if maxHR < 0 {
		maxHR = 0
	}

	// Read current profile to preserve fields not on this form (age, location, etc.)
	a, err := th.db.GetAthlete()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	a.FTPWatts = ftp
	a.WeightKG = weightKG
	a.ThresholdHR = thresholdHR
	a.MaxHR = maxHR

	if err := th.db.UpdateAthlete(a); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// addFTPHistory inserts a manually-entered FTP change point. The user is
// expected to click "recompute TSS" afterward to apply the change to existing
// workouts — recompute is intentionally not auto-fired since it walks every
// power workout.
func (th *templateHandler) addFTPHistory(w http.ResponseWriter, r *http.Request) {
	ftp, err := strconv.Atoi(r.FormValue("ftp_watts"))
	if err != nil || ftp <= 0 || ftp > 600 {
		http.Error(w, "invalid ftp_watts", http.StatusBadRequest)
		return
	}
	dateStr := r.FormValue("effective_from")
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		http.Error(w, "invalid effective_from (expect YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	if err := th.db.LogFTPChangeAt(ftp, t); err != nil {
		slog.Error("add ftp history", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings#ftp-history", http.StatusSeeOther)
}

func (th *templateHandler) deleteFTPHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := th.db.DeleteFTPHistoryEntry(id); err != nil {
		slog.Error("delete ftp history", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings#ftp-history", http.StatusSeeOther)
}

func (th *templateHandler) saveMileageGoal(w http.ResponseWriter, r *http.Request) {
	sport := r.FormValue("sport")
	switch sport {
	case "cycling", "running", "swimming":
	default:
		http.Error(w, "invalid sport", http.StatusBadRequest)
		return
	}

	weeklyDisplay, err := strconv.ParseFloat(r.FormValue("weekly"), 64)
	if err != nil || weeklyDisplay < 0 {
		weeklyDisplay = 0
	}
	yearlyDisplay, err := strconv.ParseFloat(r.FormValue("yearly"), 64)
	if err != nil || yearlyDisplay < 0 {
		yearlyDisplay = 0
	}

	var weeklyMeters, yearlyMeters float64
	if th.isImperial() {
		weeklyMeters = weeklyDisplay * 1609.344
		yearlyMeters = yearlyDisplay * 1609.344
	} else {
		weeklyMeters = weeklyDisplay * 1000
		yearlyMeters = yearlyDisplay * 1000
	}

	if err := th.db.SaveMileageGoal(sport, weeklyMeters, yearlyMeters); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?goal_sport="+sport, http.StatusSeeOther) // nosemgrep: go.lang.security.injection.open-redirect.open-redirect
}

func resolveHRZones(a *models.Athlete) []models.HRZone {
	return fitness.ResolveHRZones(a)
}

func (th *templateHandler) saveHRZones(w http.ResponseWriter, r *http.Request) {
	var maxBPMs [6]int
	// Z1–Z4 upper bounds; Z5 is always open-ended (Coggan 5-zone model)
	fields := []string{"z1_max", "z2_max", "z3_max", "z4_max"}
	for i, f := range fields {
		v, err := strconv.Atoi(r.FormValue(f))
		if err != nil || v <= 0 {
			http.Error(w, "invalid value for "+f, http.StatusBadRequest)
			return
		}
		maxBPMs[i] = v
	}
	// Validate zones are strictly ascending
	for i := 1; i < 4; i++ {
		if maxBPMs[i] <= maxBPMs[i-1] {
			http.Error(w, "zone boundaries must be strictly ascending", http.StatusBadRequest)
			return
		}
	}
	zonesJSON := fmt.Sprintf("[%d,%d,%d,%d,0,0]",
		maxBPMs[0], maxBPMs[1], maxBPMs[2], maxBPMs[3])
	if err := th.db.SetCustomHRZones(zonesJSON); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (th *templateHandler) resetHRZones(w http.ResponseWriter, r *http.Request) {
	if err := th.db.ClearCustomHRZones(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// saveIntegrationCredentials returns a handler that saves the client ID and secret
// for the named integration and redirects back to settings.
func (th *templateHandler) saveIntegrationCredentials(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientID := r.FormValue("client_id")
		clientSecret := r.FormValue("client_secret")
		if clientID == "" || clientSecret == "" {
			http.Error(w, "client_id and client_secret are required", http.StatusBadRequest)
			return
		}
		if err := th.db.SetIntegrationCredentials(name, clientID, clientSecret); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

// saveDropboxCredentials stores the Dropbox access token and folder path.
// It validates the token before saving.
func (th *templateHandler) saveDropboxCredentials(w http.ResponseWriter, r *http.Request) {
	accessToken := r.FormValue("access_token")
	folderPath := r.FormValue("folder_path")
	if accessToken == "" {
		http.Error(w, "access_token is required", http.StatusBadRequest)
		return
	}
	if folderPath == "" {
		folderPath = "/Apps/WahooFitness"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := dropbox.New(accessToken).ValidateToken(ctx); err != nil {
		http.Error(w, "invalid Dropbox token: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := th.db.SetIntegrationToken("dropbox", accessToken); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if err := th.db.SetIntegrationCredentials("dropbox", folderPath, ""); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// saveIntervalsCredentials stores the intervals.icu athlete ID and API key.
// It validates the credentials before saving.
func (th *templateHandler) saveIntervalsCredentials(w http.ResponseWriter, r *http.Request) {
	athleteID := r.FormValue("athlete_id")
	apiKey := r.FormValue("api_key")
	if athleteID == "" || apiKey == "" {
		http.Error(w, "athlete_id and api_key are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := intervals.New(athleteID, apiKey).ValidateCredentials(ctx); err != nil {
		http.Error(w, "invalid intervals.icu credentials: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := th.db.SetIntegrationCredentials("intervals", athleteID, apiKey); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// saveAISettings stores the selected AI provider, model, and API key.
func (th *templateHandler) saveAISettings(w http.ResponseWriter, r *http.Request) {
	provider := r.FormValue("provider")
	model := r.FormValue("model")
	apiKey := r.FormValue("api_key")

	if _, ok := aicoach.Get(provider); !ok {
		http.Error(w, "invalid provider", http.StatusBadRequest)
		return
	}
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	// A blank key from the "update settings" form means "keep the existing key" —
	// the user is just changing the model. That only holds within one provider:
	// a key for provider A won't authenticate against provider B, so a provider
	// switch always requires a fresh key.
	if apiKey == "" {
		existing, err := th.db.GetAISettings()
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if existing.APIKey == "" {
			http.Error(w, "api_key is required", http.StatusBadRequest)
			return
		}
		if existing.Provider != provider {
			http.Error(w, "api_key is required when changing provider", http.StatusBadRequest)
			return
		}
		apiKey = existing.APIKey
	}

	if err := th.db.SaveAISettings(db.AISettings{
		Provider: provider,
		Model:    model,
		APIKey:   apiKey,
	}); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	// Clear any cached insights — they were generated with the previous config.
	_ = th.db.ClearCachedInsights()
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
