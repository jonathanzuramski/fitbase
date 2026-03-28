package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func NewRouter(h *Handler, dropbox *DropboxHandler, intervalsH *IntervalsHandler, gdrive *GDriveHandler, staticFS http.FileSystem, templateHandler http.Handler) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.CleanPath)

	r.Route("/api", func(r chi.Router) {
		r.Get("/workouts", h.ListWorkouts)
		r.Delete("/workouts", h.DeleteAllWorkouts)
		r.Post("/upload", h.Upload)

		r.Route("/workouts/{id}", func(r chi.Router) {
			r.Get("/", h.GetWorkout)
			r.Get("/streams", h.GetStreams)
			r.Get("/summary", h.GetWorkoutSummary)
			r.Get("/analysis", h.GetWorkoutAnalysis)
			r.Get("/download", h.DownloadFIT)
			r.Get("/route", h.GetWorkoutRoute)
			r.Delete("/", h.DeleteWorkout)
		})

		r.Get("/athlete", h.GetAthlete)
		r.Put("/athlete", h.UpdateAthlete)
		r.Get("/athlete/zones", h.GetAthleteZones)
		r.Get("/athlete/power-curve", h.GetPowerCurve)
		r.Get("/athlete/readiness", h.GetReadiness)

		r.Get("/fitness", h.GetFitness)
		r.Get("/training/weekly", h.GetWeeklyTraining)

		r.Route("/integrations/dropbox", func(r chi.Router) {
			r.Get("/sync", dropbox.Sync)
			r.Delete("/", dropbox.Disconnect)
			r.Post("/longpoll", dropbox.SetLongpoll)
		})

		r.Route("/integrations/intervals", func(r chi.Router) {
			r.Get("/sync", intervalsH.Sync)
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
	r.Mount("/", templateHandler)

	return r
}
