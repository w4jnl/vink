package api

import (
	"net/http"
	"net/url"

	"github.com/w4jnl/vink/internal/adminapi"
	"github.com/w4jnl/vink/internal/http/middleware"
)

// mountAdmin registers /api/v1/admin: instance administration for
// instance admin keys and instance admins' sessions. Handlers decode,
// call adminapi.Direct as the caller, and encode; vink admin on the
// server host runs the same Direct.
func (a *API) mountAdmin(mux *http.ServeMux) {
	a.registerAdmin(mux, "GET", "/orgs", a.adminListOrgs)
	a.registerAdmin(mux, "POST", "/orgs", a.adminCreateOrg)
	a.registerAdmin(mux, "GET", "/orgs/{org}", a.adminGetOrg)
	a.registerAdmin(mux, "PATCH", "/orgs/{org}", a.adminUpdateOrg)
	a.registerAdmin(mux, "DELETE", "/orgs/{org}", a.adminDeleteOrg)
	a.registerAdmin(mux, "GET", "/orgs/{org}/keys", a.adminListOrgKeys)
	a.registerAdmin(mux, "POST", "/orgs/{org}/keys", a.adminCreateOrgKey)
	a.registerAdmin(mux, "DELETE", "/orgs/{org}/keys/{id}", a.adminRevokeOrgKey)
	a.registerAdmin(mux, "GET", "/orgs/{org}/agents", a.adminListAgents)
	a.registerAdmin(mux, "POST", "/orgs/{org}/agents", a.adminCreateAgent)
	a.registerAdmin(mux, "DELETE", "/orgs/{org}/agents/{name}", a.adminRevokeAgent)
	a.registerAdmin(mux, "GET", "/users", a.adminListUsers)
	a.registerAdmin(mux, "POST", "/users", a.adminCreateUser)
	a.registerAdmin(mux, "GET", "/users/{user}", a.adminGetUser)
	a.registerAdmin(mux, "PATCH", "/users/{user}", a.adminUpdateUser)
	a.registerAdmin(mux, "POST", "/users/{user}/totp-reset", a.adminResetTOTP)
	a.registerAdmin(mux, "POST", "/users/{user}/reset-link", a.adminResetLink)
	a.registerAdmin(mux, "PUT", "/users/{user}/orgs/{org}", a.adminGrant)
	a.registerAdmin(mux, "DELETE", "/users/{user}/orgs/{org}", a.adminUngrant)
	a.registerAdmin(mux, "GET", "/keys", a.listAdminKeys)
	a.registerAdmin(mux, "DELETE", "/keys/{id}", a.revokeAdminKey)
}

// direct runs admin actions as the request's caller.
func (a *API) direct(r *http.Request) adminapi.Direct {
	return adminapi.Direct{Svc: a.svc, Scope: scope(r)}
}

// adminLocation is the address of a created admin resource.
func adminLocation(r *http.Request, parts ...string) string {
	p := Prefix + "/admin"
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return middleware.Href(r, p)
}

// --- orgs -----------------------------------------------------------------

func (a *API) adminListOrgs(w http.ResponseWriter, r *http.Request) error {
	orgs, err := a.direct(r).ListOrgs(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page[adminapi.Org]{Items: orgs})
	return nil
}

func (a *API) adminCreateOrg(w http.ResponseWriter, r *http.Request) error {
	var in adminapi.OrgCreate
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	org, err := a.direct(r).CreateOrg(r.Context(), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", adminLocation(r, "orgs", org.Slug))
	writeJSON(w, http.StatusCreated, org)
	return nil
}

func (a *API) adminGetOrg(w http.ResponseWriter, r *http.Request) error {
	org, err := a.direct(r).GetOrg(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, org)
	return nil
}

func (a *API) adminUpdateOrg(w http.ResponseWriter, r *http.Request) error {
	var in adminapi.OrgPatch
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	org, err := a.direct(r).UpdateOrg(r.Context(), r.PathValue("org"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, org)
	return nil
}

func (a *API) adminDeleteOrg(w http.ResponseWriter, r *http.Request) error {
	if err := a.direct(r).DeleteOrg(r.Context(), r.PathValue("org")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- org keys -------------------------------------------------------------

func (a *API) adminListOrgKeys(w http.ResponseWriter, r *http.Request) error {
	keys, err := a.direct(r).ListOrgKeys(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page[adminapi.OrgKey]{Items: keys})
	return nil
}

func (a *API) adminCreateOrgKey(w http.ResponseWriter, r *http.Request) error {
	var in adminapi.KeyCreate
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	k, err := a.direct(r).CreateOrgKey(r.Context(), r.PathValue("org"), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", adminLocation(r, "orgs", r.PathValue("org"), "keys", k.ID))
	writeJSON(w, http.StatusCreated, k)
	return nil
}

func (a *API) adminRevokeOrgKey(w http.ResponseWriter, r *http.Request) error {
	if err := a.direct(r).RevokeOrgKey(r.Context(), r.PathValue("org"), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- agents ---------------------------------------------------------------

func (a *API) adminListAgents(w http.ResponseWriter, r *http.Request) error {
	agents, err := a.direct(r).ListAgents(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page[adminapi.Agent]{Items: agents})
	return nil
}

func (a *API) adminCreateAgent(w http.ResponseWriter, r *http.Request) error {
	var in adminapi.AgentCreate
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ag, err := a.direct(r).CreateAgent(r.Context(), r.PathValue("org"), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", adminLocation(r, "orgs", r.PathValue("org"), "agents", ag.Name))
	writeJSON(w, http.StatusCreated, ag)
	return nil
}

func (a *API) adminRevokeAgent(w http.ResponseWriter, r *http.Request) error {
	if err := a.direct(r).RevokeAgent(r.Context(), r.PathValue("org"), r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- users ----------------------------------------------------------------

func (a *API) adminListUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := a.direct(r).ListUsers(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page[adminapi.User]{Items: users})
	return nil
}

func (a *API) adminCreateUser(w http.ResponseWriter, r *http.Request) error {
	var in adminapi.UserCreate
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	u, err := a.direct(r).CreateUser(r.Context(), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", adminLocation(r, "users", u.Subject))
	writeJSON(w, http.StatusCreated, u)
	return nil
}

func (a *API) adminGetUser(w http.ResponseWriter, r *http.Request) error {
	u, err := a.direct(r).GetUser(r.Context(), r.PathValue("user"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

func (a *API) adminUpdateUser(w http.ResponseWriter, r *http.Request) error {
	var in adminapi.UserPatch
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	u, err := a.direct(r).UpdateUser(r.Context(), r.PathValue("user"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

func (a *API) adminResetTOTP(w http.ResponseWriter, r *http.Request) error {
	if err := a.direct(r).ResetTOTP(r.Context(), r.PathValue("user")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *API) adminResetLink(w http.ResponseWriter, r *http.Request) error {
	link, err := a.direct(r).CreateResetLink(r.Context(), r.PathValue("user"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, link)
	return nil
}

func (a *API) adminGrant(w http.ResponseWriter, r *http.Request) error {
	var in adminapi.RoleSet
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	u, err := a.direct(r).Grant(r.Context(), r.PathValue("user"), r.PathValue("org"), in.Role)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

func (a *API) adminUngrant(w http.ResponseWriter, r *http.Request) error {
	if err := a.direct(r).Ungrant(r.Context(), r.PathValue("user"), r.PathValue("org")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- admin keys -----------------------------------------------------------

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
