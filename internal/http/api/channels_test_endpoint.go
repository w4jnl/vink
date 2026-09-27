package api

import (
	"errors"
	"net/http"

	"github.com/w4jnl/vink/internal/domain"
)

// testChannel sends a synthetic notification through the channel and
// returns the notifier's error verbatim, so the UI can show it.
func (a *API) testChannel(w http.ResponseWriter, r *http.Request) error {
	err := a.svc.TestChannel(r.Context(), scope(r), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return nil
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		return err
	default:
		if _, ok := domain.AsValidation(err); ok {
			return err
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return nil
	}
}
