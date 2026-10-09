package api

import (
	"errors"
	"net/http"

	"github.com/w4jnl/vink/internal/domain"
)

// testChannel sends a synthetic notification through the channel and
// returns the notifier's error verbatim, so the UI can show it.
func (a *API) testChannel(w http.ResponseWriter, r *http.Request) error {
	return testResult(w, a.svc.TestChannel(r.Context(), scope(r), r.PathValue("id")))
}

// testOrgChannel is testChannel for a channel of the org.
func (a *API) testOrgChannel(w http.ResponseWriter, r *http.Request) error {
	return testResult(w, a.svc.TestOrgChannel(r.Context(), scope(r), r.PathValue("id")))
}

// testResult answers a channel test: 200 when it went out, the problem for
// a missing channel, a refusal or a bad config, else 502 with the
// notifier's error.
func testResult(w http.ResponseWriter, err error) error {
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
