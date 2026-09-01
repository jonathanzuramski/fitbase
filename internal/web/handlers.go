package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/fitbase/fitbase/internal/db"
)

// This file holds the shared plumbing for the server-rendered UI plus the
// trivial pages. Each substantial page lives in its own file — dashboard.go
// (index), workout_page.go, settings.go, welcome.go, calendar.go — following
// the api package's one-file-per-concern layout. Every page passes a typed
// view struct to its template, so a template referencing a field the handler
// forgot to supply is a compile or render error, never a silently-blank value.

type pageTemplates struct {
	index     *template.Template
	workout   *template.Template
	settings  *template.Template
	welcome   *template.Template
	calendar  *template.Template
	heatmap   *template.Template
	importing *template.Template
	progress  *template.Template
}

// fsys is used just so that updates in local dev are picked up
// and rendered without restarting the go binary every time.
func loadTemplatesFrom(fsys fs.FS) *pageTemplates {
	parse := func(files ...string) *template.Template {
		return template.Must(
			template.New("").Funcs(FuncMap).ParseFS(fsys, files...),
		)
	}
	return &pageTemplates{
		index:     parse("templates/base.html", "templates/index.html"),
		workout:   parse("templates/base.html", "templates/workout.html"),
		settings:  parse("templates/base.html", "templates/settings.html"),
		welcome:   parse("templates/welcome.html"),
		calendar:  parse("templates/base.html", "templates/calendar.html"),
		heatmap:   parse("templates/base.html", "templates/heatmap.html"),
		importing: parse("templates/importing.html"),
	}
}

// templateHandler serves server-rendered UI pages.
type templateHandler struct {
	tmpls *pageTemplates
	db    *db.DB
	dev   bool
	webFS fs.FS // only used when dev=true to re-parse templates per request
}

func (th *templateHandler) templates() *pageTemplates {
	if th.dev {
		return loadTemplatesFrom(th.webFS)
	}
	return th.tmpls
}

// NewTemplateHandler creates the http.Handler for all server-rendered pages.
func NewTemplateHandler(database *db.DB, dev bool, webFS fs.FS) http.Handler {
	th := &templateHandler{
		tmpls: loadTemplatesFrom(webFS),
		db:    database,
		dev:   dev,
		webFS: webFS,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", th.index)
	mux.HandleFunc("/workouts/{id}", th.workout)
	mux.HandleFunc("GET /settings", th.settings)
	mux.HandleFunc("POST /settings/units", th.setUnits)
	mux.HandleFunc("POST /settings/athlete", th.updateAthlete)
	mux.HandleFunc("POST /settings/hr-zones", th.saveHRZones)
	mux.HandleFunc("POST /settings/hr-zones/reset", th.resetHRZones)
	mux.HandleFunc("POST /settings/ftp-history", th.addFTPHistory)
	mux.HandleFunc("POST /settings/ftp-history/delete", th.deleteFTPHistory)
	mux.HandleFunc("POST /settings/integrations/dropbox/credentials", th.saveDropboxCredentials)
	mux.HandleFunc("POST /settings/integrations/intervals/credentials", th.saveIntervalsCredentials)
	mux.HandleFunc("POST /settings/integrations/gdrive/credentials", th.saveIntegrationCredentials("gdrive"))
	mux.HandleFunc("POST /goals/mileage", th.saveMileageGoal)
	mux.HandleFunc("GET /heatmap", th.heatmap)
	mux.HandleFunc("POST /settings/ai", th.saveAISettings)
	mux.HandleFunc("GET /calendar", th.calendar)
	mux.HandleFunc("GET /importing", th.importing)
	mux.HandleFunc("GET /welcome", th.welcomeGet)
	mux.HandleFunc("POST /welcome", th.welcomePost)
	mux.HandleFunc("GET /welcome/skip", th.welcomeSkip)
	return mux
}

// renderTemplate executes a template into a buffer first. On success the
// buffered HTML is written to w; on failure a 500 is returned so the client
// never sees a partial page with a 200 status.
func renderTemplate(w http.ResponseWriter, t *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render template", "name", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// isImperial reads the units preference from the athlete profile.
// Defaults to imperial if the profile is unavailable.
func (th *templateHandler) isImperial() bool {
	a, err := th.db.GetAthlete()
	if err != nil || a == nil {
		return true
	}
	return a.Units != "metric"
}

// heatmapView is the template data for the heatmap page; the map itself is
// drawn client-side from /api/workouts/routes.
type heatmapView struct {
	Imperial bool
}

func (th *templateHandler) heatmap(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, th.templates().heatmap, "base", heatmapView{Imperial: th.isImperial()})
}

// importing serves the standalone first-run import screen. The API router
// redirects all page requests here while a startup archive reimport is active;
// the page polls /api/import/status and returns to the app when it finishes.
func (th *templateHandler) importing(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, th.templates().importing, "importing", nil)
}
