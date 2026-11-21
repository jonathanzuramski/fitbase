package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/models"
)

func TestWorkoutFeedbackAPI(t *testing.T) {
	h, d := newTestHandler(t)
	insertSampleWorkout(t, d, "w1")

	put := func(id, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/workouts/"+id+"/feedback", strings.NewReader(body)), "id", id)
		h.SetWorkoutFeedback(rr, req)
		return rr
	}
	for body, want := range map[string]int{
		`{"rpe":0}`:  http.StatusBadRequest,
		`{"rpe":11}`: http.StatusBadRequest,
		`{"note":"` + strings.Repeat("x", models.MaxFeedbackNote+1) + `"}`: http.StatusBadRequest,
		`nope`: http.StatusBadRequest,
	} {
		if rr := put("w1", body); rr.Code != want {
			t.Errorf("PUT %.30s: %d, want %d", body, rr.Code, want)
		}
	}
	if rr := put("missing", `{"rpe":5}`); rr.Code != http.StatusNotFound {
		t.Errorf("PUT on a missing workout: %d, want 404", rr.Code)
	}

	rr := put("w1", `{"rpe":7,"note":"windy"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rr.Code, rr.Body.String())
	}
	var f models.WorkoutFeedback
	decodeEnvelope(t, rr.Body.Bytes(), &f)
	if f.RPE == nil || *f.RPE != 7 || f.Note != "windy" || f.WorkoutID != "w1" {
		t.Errorf("PUT response = %+v", f)
	}

	get := httptest.NewRecorder()
	h.GetWorkoutFeedback(get, withURLParam(httptest.NewRequest(http.MethodGet, "/api/workouts/w1/feedback", nil), "id", "w1"))
	decodeEnvelope(t, get.Body.Bytes(), &f)
	if get.Code != http.StatusOK || f.RPE == nil || *f.RPE != 7 {
		t.Errorf("GET = %d %+v", get.Code, f)
	}
}

// A ride without feedback answers with no rating record: the body used to
// carry updated_at "0001-01-01T00:00:00Z", a rating dated year 1.
func TestWorkoutFeedbackAPI_NoneHasNoUpdatedAt(t *testing.T) {
	h, d := newTestHandler(t)
	insertSampleWorkout(t, d, "w1")
	req := func(method, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := withURLParam(httptest.NewRequest(method, "/api/workouts/w1/feedback", strings.NewReader(body)), "id", "w1")
		if method == http.MethodPut {
			h.SetWorkoutFeedback(rr, r)
		} else {
			h.GetWorkoutFeedback(rr, r)
		}
		return rr
	}
	if rr := req(http.MethodGet, ""); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "updated_at") {
		t.Errorf("GET with no feedback = %d %s, want no updated_at", rr.Code, rr.Body.String())
	}
	if rr := req(http.MethodPut, `{"rpe":6}`); !strings.Contains(rr.Body.String(), `"updated_at"`) {
		t.Errorf("PUT with a rating = %s, want updated_at", rr.Body.String())
	}
	if rr := req(http.MethodPut, `{"rpe":null,"note":""}`); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "updated_at") {
		t.Errorf("clearing PUT = %d %s, want no updated_at", rr.Code, rr.Body.String())
	}
}
