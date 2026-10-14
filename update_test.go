package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReleaseVersion(t *testing.T) {
	valid, err := parseReleaseVersion("v12.34.56")
	if err != nil {
		t.Fatal(err)
	}
	if valid != (releaseVersion{major: 12, minor: 34, patch: 56}) {
		t.Fatalf("version = %+v", valid)
	}
	for _, input := range []string{"1.2", "1.2.3.4", "1.02.3", "1.2.-1", "latest"} {
		if _, err := parseReleaseVersion(input); err == nil {
			t.Fatalf("accepted invalid version %q", input)
		}
	}
}

func TestUpdateExecutableReplacesVerifiedBinary(t *testing.T) {
	current, err := parseReleaseVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	latest := fmt.Sprintf("%d.%d.%d", current.major, current.minor, current.patch+1)
	asset, err := updateAssetName()
	if err != nil {
		t.Fatal(err)
	}
	nextBinary := []byte("verified replacement")
	sum := sha256.Sum256(nextBinary)

	server := newUpdateServer(t, latest, asset, fmt.Sprintf("%x  %s\n", sum, asset), nextBinary)
	executable := filepath.Join(t.TempDir(), "rs")
	if err := os.WriteFile(executable, []byte("current binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	updated, gotVersion, err := updateExecutable(
		context.Background(),
		server.Client(),
		server.URL+"/latest",
		server.URL+"/releases/download",
		executable,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !updated || gotVersion != latest {
		t.Fatalf("updated = %v, version = %q", updated, gotVersion)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(nextBinary) {
		t.Fatalf("binary = %q", data)
	}
}

func TestUpdateExecutableDoesNotWriteWhenCurrent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path == "/latest" {
			http.Redirect(w, request, "/releases/tag/v"+version, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	executable := filepath.Join(t.TempDir(), "missing")
	updated, gotVersion, err := updateExecutable(
		context.Background(),
		server.Client(),
		server.URL+"/latest",
		server.URL+"/releases/download",
		executable,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated || gotVersion != version {
		t.Fatalf("updated = %v, version = %q", updated, gotVersion)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want latest redirect only", requests)
	}
	if _, err := os.Stat(executable); !os.IsNotExist(err) {
		t.Fatalf("current-version check wrote executable: %v", err)
	}
}

func TestUpdateExecutablePreservesBinaryOnChecksumMismatch(t *testing.T) {
	current, err := parseReleaseVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	latest := fmt.Sprintf("%d.%d.%d", current.major, current.minor, current.patch+1)
	asset, err := updateAssetName()
	if err != nil {
		t.Fatal(err)
	}
	server := newUpdateServer(t, latest, asset, strings.Repeat("0", 64)+"  "+asset+"\n", []byte("untrusted"))
	executable := filepath.Join(t.TempDir(), "rs")
	if err := os.WriteFile(executable, []byte("current binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := updateExecutable(
		context.Background(),
		server.Client(),
		server.URL+"/latest",
		server.URL+"/releases/download",
		executable,
	); err == nil {
		t.Fatal("checksum mismatch succeeded")
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "current binary" {
		t.Fatalf("binary changed to %q", data)
	}
}

func newUpdateServer(t *testing.T, latest, asset, checksums string, binary []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/latest":
			http.Redirect(w, request, "/releases/tag/v"+latest, http.StatusFound)
		case "/releases/tag/v" + latest:
			w.WriteHeader(http.StatusOK)
		case "/releases/download/v" + latest + "/SHA256SUMS":
			_, _ = w.Write([]byte(checksums))
		case "/releases/download/v" + latest + "/" + asset:
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
