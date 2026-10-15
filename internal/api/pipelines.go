package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// MaxPipelineBytes is the largest pipeline file the check endpoint accepts.
const MaxPipelineBytes = 1 << 20

// checkResult is the response of POST /api/v1/pipelines/check.
type checkResult struct {
	Valid  bool             `json:"valid"`
	Order  []string         `json:"order,omitempty"`
	Errors []pipeline.Error `json:"errors,omitempty"`
}

// checkPipeline validates the pipeline file sent as the request body.
// An invalid pipeline is a normal result, not an HTTP error.
func checkPipeline(w http.ResponseWriter, r *http.Request) {
	src, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPipelineBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "pipeline file is larger than 1 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}

	p, err := pipeline.Parse(src)
	if err != nil {
		var verr *pipeline.ValidationError
		if !errors.As(err, &verr) {
			writeError(w, http.StatusInternalServerError, "internal", "pipeline check failed")
			return
		}
		writeJSON(w, http.StatusOK, checkResult{Valid: false, Errors: verr.Errors})
		return
	}
	writeJSON(w, http.StatusOK, checkResult{Valid: true, Order: p.Order()})
}
