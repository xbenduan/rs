package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/rs/internal/protocol"
)

func TestHTTPClaimReturnsCiphertextAndLoginWorks(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	state, claimURL, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(auth.HTTPHandler())
	defer httpServer.Close()
	token := strings.TrimPrefix(claimURL, "https://rs.example.com/claim/")

	// Link previews and scanners fetch with GET/HEAD; neither may consume it.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request, _ := http.NewRequest(method, httpServer.URL+"/claim/"+token, nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s claim status = %d, want 405", method, response.StatusCode)
		}
	}

	claimResponse, err := http.Post(httpServer.URL+"/claim/"+token, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if claimResponse.StatusCode != http.StatusOK {
		t.Fatalf("claim status = %d", claimResponse.StatusCode)
	}
	if claimResponse.Header.Get("Cache-Control") != "no-store" ||
		claimResponse.Header.Get("Content-Disposition") == "" ||
		claimResponse.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("claim response is missing private download headers")
	}
	var envelope protocol.ClaimEnvelope
	if err := json.NewDecoder(claimResponse.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	_ = claimResponse.Body.Close()
	body, _ := json.Marshal(envelope)
	if bytes.Contains(body, []byte("correct-horse-battery-staple")) {
		t.Fatal("claim response contains plaintext password")
	}
	bundle, err := protocol.OpenClaim(envelope, token)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Node.Password != "correct-horse-battery-staple" {
		t.Fatal("client could not decrypt claim")
	}

	claimResponse, err = http.Post(httpServer.URL+"/claim/"+token, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = claimResponse.Body.Close()
	if claimResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("second claim status = %d, want 404", claimResponse.StatusCode)
	}

	loginBody, _ := json.Marshal(loginRequest{Password: "correct-horse-battery-staple"})
	response, err := http.Post(
		httpServer.URL+"/r/"+state.AccountID+"/session",
		"application/json",
		bytes.NewReader(loginBody),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
}

func TestHTTPRejectsUnknownJSON(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	state, _, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/r/"+state.AccountID+"/session",
		bytes.NewBufferString(`{"password":"correct-horse-battery-staple","extra":true}`),
	)
	recorder := httptest.NewRecorder()
	auth.HTTPHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}
