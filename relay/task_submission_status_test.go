package relay

import (
	"net/http"
	"testing"
)

// Getting this wrong in the rejecting direction is costly: the upstream starts
// the job and bills for it while we store nothing, so the task can neither be
// polled nor refunded. 202 is the case that actually bit us.
func TestSubmissionAccepted(t *testing.T) {
	accepted := []int{
		http.StatusOK,        // 200 — the common case
		http.StatusCreated,   // 201
		http.StatusAccepted,  // 202 — asynchronous job taken
		http.StatusNoContent, // 204
		299,                  // top of the 2xx range
	}
	for _, code := range accepted {
		if !submissionAccepted(code) {
			t.Errorf("status %d treated as failure; the upstream would bill for a task we never stored", code)
		}
	}

	rejected := []int{
		199,
		http.StatusMultipleChoices,     // 300
		http.StatusBadRequest,          // 400
		http.StatusUnauthorized,        // 401
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		0,                              // no response recorded
	}
	for _, code := range rejected {
		if submissionAccepted(code) {
			t.Errorf("status %d treated as accepted; a failed submission would be stored as a live task", code)
		}
	}
}
