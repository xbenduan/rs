package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const maxAuthBody = 64 << 10

type loginRequest struct {
	Password string `json:"password"`
}

// HTTPHandler exposes credential claim and password login endpoints.
func (a *AuthManager) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	// Claim consumes state, so it must not answer GET: link previews and
	// scanners that fetch a pasted URL would burn the one-time link.
	mux.HandleFunc("POST /claim/{token}", a.handleClaim)
	mux.HandleFunc("POST /r/{account}/session", a.handleLogin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (a *AuthManager) handleClaim(w http.ResponseWriter, r *http.Request) {
	bundle, err := a.Claim(r.PathValue("token"))
	if err != nil {
		if errors.Is(err, ErrInvalidClaim) {
			writeError(w, http.StatusNotFound, "invalid or consumed claim URL")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not consume claim")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="rs-claim.json"`)
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	writeJSON(w, http.StatusOK, bundle)
}

func (a *AuthManager) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	accountID := r.PathValue("account")
	response, err := a.Login(accountID, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid account or password")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
