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
			r.Get("/download", h.DownloadFIT)
			r.Get("/route", h.GetWorkoutRoute)
			r.Delete("/", h.DeleteWorkout)
		})

		r.Route("/athlete", func(r chi.Router) {
			r.Get("/", h.GetAthlete)
			r.Put("/", h.UpdateAthlete)
			r.Get("/zones", h.GetAthleteZones)
			r.Get("/power-curve", h.GetPowerCurve)
			r.Get("/readiness", h.GetReadiness)
		})

		r.Get("/fitness", h.GetFitness)
		r.Get("/training/weekly", h.GetWeeklyTraining)
		r.Get("/ftp-history/recompute", h.RecomputePowerLoad)
		r.Get("/coach/insights", coach.GetCachedInsights)
		r.Post("/coach/insights", coach.GenerateInsights)
		r.Post("/coach/chat", coach.Chat)
		r.Post("/coach/models", coach.ListModels)

		r.Route("/planned-workouts", func(r chi.Router) {
			r.Get("/", planned.List)
			r.Post("/", planned.Create)
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

	r.Handle("/static/*", http.StripPrefix("/static", http.FileServer(staticFS)))
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
