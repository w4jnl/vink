// Package api serves /api/v1: the product's real interface. Handlers are
// thin: parse, call one service method, render JSON. Errors are RFC 7807
// problem documents.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
)

const problemBase = "https://github.com/w4jnl/vink/blob/main/docs/errors.md#"

// Problem is an RFC 7807 document.
type Problem struct {
	Type     string              `json:"type"`
	Title    string              `json:"title"`
	Status   int                 `json:"status"`
	Detail   string              `json:"detail,omitempty"`
	Instance string              `json:"instance,omitempty"`
	Errors   []domain.FieldError `json:"errors,omitempty"`
}

// errBadRequest marks a malformed request body or parameter.
type errBadRequest struct{ msg string }

func (e errBadRequest) Error() string { return e.msg }

func badRequest(format string, a ...any) error { return errBadRequest{msg: fmt.Sprintf(format, a...)} }

// writeError maps an error to a problem response.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	p := Problem{Instance: middleware.Href(r, r.URL.Path)}
	var br errBadRequest
	switch {
	case errors.As(err, &br):
		p.Type, p.Title, p.Status, p.Detail = "malformed", "Malformed request", http.StatusBadRequest, br.msg
	case errors.Is(err, domain.ErrUnauthorized):
		p.Type, p.Title, p.Status, p.Detail = "unauthorized", "Authentication required", http.StatusUnauthorized, "send a valid bearer API key or sign in"
		w.Header().Set("WWW-Authenticate", `Bearer realm="vink"`)
	case errors.Is(err, domain.ErrForbidden):
		p.Type, p.Title, p.Status, p.Detail = "forbidden", "Forbidden", http.StatusForbidden, err.Error()
	case errors.Is(err, domain.ErrNotFound):
		p.Type, p.Title, p.Status, p.Detail = "not-found", "Not found", http.StatusNotFound, err.Error()
	case errors.Is(err, domain.ErrConflict):
		p.Type, p.Title, p.Status, p.Detail = "conflict", "Conflict", http.StatusConflict, err.Error()
	case errors.Is(err, domain.ErrRateLimited):
		p.Type, p.Title, p.Status, p.Detail = "rate-limit", "Too many requests", http.StatusTooManyRequests, "slow down"
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, domain.ErrMethodNotAllowed):
		p.Type, p.Title, p.Status = "method-not-allowed", "Method not allowed", http.StatusMethodNotAllowed
	default:
		if ve, ok := domain.AsValidation(err); ok {
			p.Type, p.Title, p.Status, p.Errors = "validation", "Validation failed", http.StatusUnprocessableEntity, ve.Errors
			break
		}
		p.Type, p.Title, p.Status = "internal", "Internal error", http.StatusInternalServerError
		p.Detail = "request " + middleware.GetRequestID(r.Context())
		log.Error("api error", "err", err, "path", r.URL.Path, "req_id", middleware.GetRequestID(r.Context()))
	}
	p.Type = problemBase + p.Type
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// writeJSON renders v with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// maxBody caps request bodies.
const maxBody = 1 << 20

// decodeJSON reads a JSON body into v, refusing unknown fields.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	if dec.More() {
		return badRequest("invalid JSON body: trailing data")
	}
	return nil
}
