package api

import (
	"net/http"

	"github.com/w4jnl/vink/internal/adminapi"
)

// mountAdmin registers /api/v1/admin: instance administration for
// instance admin keys and instance admins' sessions.
func (a *API) mountAdmin(mux *http.ServeMux) {
	a.registerAdmin(mux, "GET", "/keys", a.listAdminKeys)
	a.registerAdmin(mux, "DELETE", "/keys/{id}", a.revokeAdminKey)
}

// direct runs admin actions as the request's caller.
func (a *API) direct(r *http.Request) adminapi.Direct {
	return adminapi.Direct{Svc: a.svc, Scope: scope(r)}
}

// Admin keys are listed and revoked here, never created: a key is made
// only in the web UI or with vink admin key create on the server host.
func (a *API) listAdminKeys(w http.ResponseWriter, r *http.Request) error {
	keys, err := a.direct(r).ListAdminKeys(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page[adminapi.AdminKey]{Items: keys})
	return nil
}

func (a *API) revokeAdminKey(w http.ResponseWriter, r *http.Request) error {
	if err := a.direct(r).RevokeAdminKey(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
