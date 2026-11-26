package httpapi

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

func (a *App) desktopAuthorized(r *http.Request) bool {
	if a.cfg.DesktopToken == "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.DesktopToken)) == 1
}

func (a *App) handleDesktopSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !a.desktopAuthorized(r) {
		writeError(w, 401, "unauthorized")
		return
	}
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	acc, err := a.db.GetAccountByOpenID(r.Context(), r.URL.Query().Get("ref"))
	if err != nil {
		writeError(w, 404, "account not found")
		return
	}
	if acc.Credentials == nil || (acc.Status != nil && *acc.Status == "expired") {
		writeError(w, 409, "account requires login")
		return
	}
	writeJSON(w, 200, map[string]any{"credentials": acc.Credentials, "login_buffer": acc.LoginBuffer, "account": acc.Public()})
}

func (a *App) handleDesktopShutdown(w http.ResponseWriter, r *http.Request) {
	if !a.desktopAuthorized(r) {
		writeError(w, 401, "unauthorized")
		return
	}
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	if a.cfg.Shutdown == nil {
		writeError(w, 409, "shutdown unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"stopping": true})
	go a.cfg.Shutdown()
}
