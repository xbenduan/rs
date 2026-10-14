package main

import (
	"context"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/rs/internal/protocol"
	"atlas/rs/server"
)

func TestInitCanHideClaimURLInPrivateFile(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	claimPath := filepath.Join(dir, "claim-url")

	output, code := captureStdout(t, func() int {
		return cmdInit([]string{
			"--name", "prod",
			"--endpoint", "https://rs.example.com",
			"--state", statePath,
			"--claim-url-file", claimPath,
		})
	})
	if code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	claimURL, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(claimURL), "https://rs.example.com/claim/") {
		t.Fatalf("claim URL = %q", claimURL)
	}
	if strings.Contains(output, strings.TrimSpace(string(claimURL))) {
		t.Fatal("init printed the one-time claim URL")
	}
	for _, path := range []string{statePath, claimPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
}

func TestCredentialResetCreatesNewLocalCredentials(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	claimPath := filepath.Join(dir, "reset-claim-url")
	oldState, _, err := server.InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"old-correct-horse-password",
	)
	if err != nil {
		t.Fatal(err)
	}

	output, code := captureStdout(t, func() int {
		return cmdCredential([]string{
			"reset",
			"--state", statePath,
			"--claim-url-file", claimPath,
		})
	})
	if code != 0 {
		t.Fatalf("credential reset exit code = %d", code)
	}
	if !strings.Contains(output, "Restart the rs service") {
		t.Fatal("reset did not require service restart")
	}
	claimData, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimURL := strings.TrimSpace(string(claimData))
	token, err := validateClaimURL(claimURL)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := server.LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccountID() == oldState.AccountID {
		t.Fatal("credential reset preserved the account ID")
	}
	envelope, err := auth.Claim(token)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := protocol.OpenClaim(envelope, token)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Node.Name != "prod" || bundle.Node.Password == "old-correct-horse-password" {
		t.Fatalf("reset bundle = %+v", bundle)
	}
}

func TestClaimDecryptsAndSavesNodeWithoutPrintingPassword(t *testing.T) {
	dir := t.TempDir()
	nodePath := filepath.Join(dir, "nodes.json")
	statePath := filepath.Join(dir, "auth.json")
	t.Setenv("RS_NODE_FILE", nodePath)

	testServer := httptest.NewUnstartedServer(nil)
	endpoint := "https://" + testServer.Listener.Addr().String()
	state, claimURL, err := server.InitializeAuth(
		statePath,
		endpoint,
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := server.LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	testServer.Config.Handler = auth.HTTPHandler()
	testServer.StartTLS()
	defer testServer.Close()

	useTestHTTPClient(t, testServer)

	output, code := captureStdout(t, func() int {
		return cmdClaim([]string{claimURL})
	})
	if code != 0 {
		t.Fatalf("claim exit code = %d", code)
	}
	node, err := findNode("prod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, node.Password) {
		t.Fatal("claim printed the node password")
	}
	if net.ParseIP(node.IP) == nil || node.ID != state.NodeID {
		t.Fatalf("node = %+v, want ID %s", node, state.NodeID)
	}
	if _, peerIP, err := authenticateNode(context.Background(), node); err != nil {
		t.Fatalf("claimed node login failed: %v", err)
	} else if peerIP != node.IP {
		t.Fatalf("saved IP = %s, login IP = %s", node.IP, peerIP)
	}
	impostor := node
	impostor.ID = "AQEBAQEBAQEBAQEBAQEBAQ"
	if _, _, err := authenticateNode(context.Background(), impostor); err == nil {
		t.Fatal("login accepted a server with a different node ID")
	}
	info, err := os.Stat(nodePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("node file mode = %o, want 600", info.Mode().Perm())
	}
	if code := cmdClaim([]string{claimURL}); code == 0 {
		t.Fatal("consumed claim URL succeeded")
	}
}

// useTestHTTPClient points login and claim requests at an httptest server.
func useTestHTTPClient(t *testing.T, testServer *httptest.Server) {
	t.Helper()
	original := httpClient
	client := *original
	client.Transport = testServer.Client().Transport
	httpClient = &client
	t.Cleanup(func() { httpClient = original })
}

func captureStdout(t *testing.T, run func() int) (string, int) {
	t.Helper()
	readOutput, writeOutput, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalOutput := os.Stdout
	os.Stdout = writeOutput
	code := run()
	_ = writeOutput.Close()
	os.Stdout = originalOutput
	output, err := io.ReadAll(readOutput)
	if err != nil {
		t.Fatal(err)
	}
	_ = readOutput.Close()
	return string(output), code
}
