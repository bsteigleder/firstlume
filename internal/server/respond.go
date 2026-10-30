package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/store"
)

const maxJSONBody = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.ErrorResponse{Error: msg})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err, "request_id", middleware.GetReqID(r.Context()))
	writeError(w, http.StatusInternalServerError, "internal error")
}

// storeError maps store errors to HTTP responses.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, what+" not found")
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, what+" already exists")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, what+" is not in a state that allows this operation")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid reference in "+what)
	default:
		s.internalError(w, r, err)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return false
	}
	return true
}
