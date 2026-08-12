package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// sseJob buffers one detached LLM run's SSE events so the browser can
// disconnect (navigate to another page) without cancelling the run, then
// re-attach and replay the whole stream. Events are kept for the job's
// lifetime; a job is retained until the next one of the same kind starts, so
// an answer that finished while nobody was watching is still recoverable.
type sseJob struct {
	ID string
	mu     sync.Mutex
	events []jobEvent
	done   bool
	wake   chan struct{} // closed and replaced on every append; closed for good on finish
}

type jobEvent struct {
	event string
	data  []byte
}

func newSSEJob() *sseJob {
	return &sseJob{ID: uuid.NewString(), wake: make(chan struct{})}
}

// emit appends one event to the buffer and wakes any attached streams.
// Events after finish are dropped.
func (j *sseJob) emit(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte(`{"error":"marshal failed"}`)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.done {
		return
	}
	j.events = append(j.events, jobEvent{event: event, data: b})
	close(j.wake)
	j.wake = make(chan struct{})
}

// finish marks the job complete and releases attached streams. Call after the
// final done/error event.
func (j *sseJob) finish() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.done {
		return
	}
	j.done = true
	close(j.wake)
}

func (j *sseJob) isDone() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

// streamTo replays the buffer from the start, then tails live events until the
// job finishes or the client disconnects. Safe for any number of concurrent
// attached clients. Appended events never mutate earlier indices, so the
// snapshot slice taken under the lock stays valid outside it.
func (j *sseJob) streamTo(w http.ResponseWriter, ctx context.Context) {
	i := 0
	for {
		j.mu.Lock()
		pending := j.events[i:]
		i = len(j.events)
		done := j.done
		wake := j.wake
		j.mu.Unlock()

		for _, e := range pending {
			writeSSERaw(w, e.event, e.data)
		}
		if done {
			return
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return
		}
	}
}

// startJob registers a new job under kind, refusing while one of that kind is
// still running. The previous finished job (and its buffered answer) is
// dropped at that point.
func (h *CoachHandler) startJob(kind string) (*sseJob, bool) {
	h.jobsMu.Lock()
	defer h.jobsMu.Unlock()
	if cur := h.jobs[kind]; cur != nil && !cur.isDone() {
		return nil, false
	}
	j := newSSEJob()
	h.jobs[kind] = j
	return j, true
}

// jobFor returns the current job of a kind, optionally requiring a specific id
// ("" matches any). nil when absent — e.g. after a server restart.
func (h *CoachHandler) jobFor(kind, id string) *sseJob {
	h.jobsMu.Lock()
	defer h.jobsMu.Unlock()
	if j := h.jobs[kind]; j != nil && (id == "" || j.ID == id) {
		return j
	}
	return nil
}

// jobStatus writes {id, done} for the current job of a kind, or 204 when there
// is none. The browser uses this on page load to decide whether a stored job
// id is still attachable.
func (h *CoachHandler) jobStatus(w http.ResponseWriter, kind string) {
	j := h.jobFor(kind, "")
	if j == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": j.ID, "done": j.isDone()})
}

// attachJob streams a job's events (replay + live tail) over SSE, 404ing when
// the id no longer matches — superseded by a newer run or lost to a restart.
func (h *CoachHandler) attachJob(w http.ResponseWriter, r *http.Request, kind, id string) {
	j := h.jobFor(kind, id)
	if j == nil {
		writeError(w, http.StatusNotFound, "no such run — it was superseded or the server restarted")
		return
	}
	setupSSE(w)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	j.streamTo(w, r.Context())
}
