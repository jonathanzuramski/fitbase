package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func NewRouter(h *Handler, dropbox *DropboxHandler, intervalsH *IntervalsHandler, gdrive *GDriveHandler, coach *CoachHandler, planned *PlannedHandler, staticFS http.FileSystem, templateHandler http.Handler) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.CleanPath)

	r.Route("/api", func(r chi.Router) {
		r.Get("/workouts", h.ListWorkouts)
		r.Delete("/workouts", h.DeleteAllWorkouts)
		r.Get("/workouts/routes", h.GetWorkoutRouteTracks)
		r.Post("/upload", h.Upload)
		r.Get("/import/status", h.ImportStatus)

		r.Route("/workouts/{id}", func(r chi.Router) {
			r.Get("/", h.GetWorkout)
			r.Get("/streams", h.GetStreams)
			r.Get("/summary", h.GetWorkoutSummary)
			r.Get("/analysis", h.GetWorkoutAnalysis)
			r.Get("/shape", h.GetWorkoutShape)
			r.Get("/download", h.DownloadFIT)
			r.Get("/route", h.GetWorkoutRoute)
			r.Get("/achievements", h.GetWorkoutAchievements)
			r.Delete("/", h.DeleteWorkout)
		})

		r.Route("/athlete", func(r chi.Router) {
			r.Get("/", h.GetAthlete)
			r.Put("/", h.UpdateAthlete)
			r.Get("/zones", h.GetAthleteZones)
			r.Get("/power-curve", h.GetPowerCurve)
			r.Get("/readiness", h.GetReadiness)
			r.Put("/ftp", h.SetFTP)
			r.Get("/ftp-check", h.GetFTPCheck)
			r.Post("/ftp-check/dismiss", h.DismissFTPCheck)
		})

		r.Get("/fitness", h.GetFitness)
		r.Get("/training/weekly", h.GetWeeklyTraining)
		r.Get("/ftp-history/recompute", h.RecomputePowerLoad)
		r.Get("/coach/insights", coach.GetCachedInsights)
		r.Post("/coach/insights", coach.GenerateInsights)
		r.Get("/coach/insights/active", coach.InsightsActive)
		r.Get("/coach/insights/{id}/events", coach.InsightsEvents)
		r.Post("/coach/chat", coach.Chat)
		r.Get("/coach/chat/active", coach.ChatActive)
		r.Get("/coach/chat/{id}/events", coach.ChatEvents)
		r.Post("/coach/models", coach.ListModels)
		r.Get("/coach/weekly-plan", coach.WeeklyPlanLatest)
		r.Post("/coach/weekly-plan", coach.WeeklyPlanGenerate)
		r.Get("/coach/weekly-plan/active", coach.WeeklyPlanActive)
		r.Get("/coach/weekly-plan/runs/{id}/events", coach.WeeklyPlanEvents)
		r.Put("/coach/weekly-plan/schedule", coach.WeeklyPlanSetSchedule)
		r.Put("/coach/weekly-plan/preferences", coach.WeeklyPlanSetPreferences)
		r.Post("/coach/weekly-plan/{id}/accept", coach.WeeklyPlanAccept)
		r.Post("/coach/weekly-plan/{id}/discard", coach.WeeklyPlanDiscard)

		r.Route("/planned-workouts", func(r chi.Router) {
			r.Get("/", planned.List)
			r.Post("/", planned.Create)
			r.Get("/{id}", planned.Get)
			r.Delete("/{id}", planned.Delete)
			r.Get("/drafts/{id}", planned.GetDraft)
			r.Post("/drafts/{id}/commit", planned.CommitDraft)
			r.Delete("/drafts/{id}", planned.DiscardDraft)
		})

		r.Route("/integrations/dropbox", func(r chi.Router) {
			r.Get("/sync", dropbox.Sync)
			r.Delete("/", dropbox.Disconnect)
			r.Post("/longpoll", dropbox.SetLongpoll)
		})

		r.Route("/integrations/intervals", func(r chi.Router) {
			r.Get("/sync", intervalsH.Sync)
			r.Get("/ftp-sync", intervalsH.FTPSync)
			r.Get("/fetch/{id}", intervalsH.Fetch)
			r.Post("/autosync", intervalsH.SetAutoSync)
			r.Delete("/", intervalsH.Disconnect)
		})

		r.Route("/integrations/gdrive", func(r chi.Router) {
			r.Get("/connect", gdrive.Connect)
			r.Get("/sync", gdrive.Sync)
			r.Delete("/", gdrive.Disconnect)
			r.Post("/restore", gdrive.Restore)
		})
	})

	r.Get("/openapi.yaml", ServeOpenAPI)
	r.Handle("/static/*", staticHandler(staticFS))
	r.Mount("/", importGate(h, templateHandler))

	return r
}

// importGate redirects page requests to the standalone /importing screen while
// a first-run archive reimport is active, so a fresh install shows progress
// instead of an empty/half-rendered app. Only wraps the page handler — /api
// and /static are matched by earlier routes — and exempts /importing itself
// to avoid a redirect loop.
func importGate(h *Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/importing" && h.importer.ReimportStatus().Active {
			http.Redirect(w, r, "/importing", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves /static/*, asking browsers to revalidate on every load.
// Dev mode serves these files from disk with a real Last-Modified, which
// browsers otherwise cache heuristically, pairing a fresh template with a stale
// stylesheet and breaking the layout. The embedded files in a release carry no
// modtime, so they were never cached and nothing changes there.
func staticHandler(fsys http.FileSystem) http.Handler {
	files := http.StripPrefix("/static", http.FileServer(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
