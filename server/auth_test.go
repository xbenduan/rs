package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atlas/rs/internal/protocol"
)

func TestAuthLifecycle(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	const password = "correct-horse-battery-staple"

	state, claimURL, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		password,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, statePath)

	auth, err := LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(claimURL, "https://rs.example.com/claim/")
	envelope, err := auth.Claim(token)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := protocol.OpenClaim(envelope, token)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Node.Name != "prod" || bundle.Node.ID != state.NodeID ||
		bundle.Node.URL != "https://rs.example.com/r/"+state.AccountID ||
		bundle.Node.Password != password {
		t.Fatalf("claim bundle = %+v", bundle)
	}
	if _, err := protocol.OpenClaim(envelope, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("claim envelope opened with wrong token")
	}

	if _, err := auth.Login(state.AccountID, "wrong-password-value"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v", err)
	}
	session, err := auth.Login(state.AccountID, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.VerifySession(session.Token); err != nil {
		t.Fatalf("fresh session rejected: %v", err)
	}
	if session.NodeID != state.NodeID || session.Version != Version {
		t.Fatalf("session = %+v", session)
	}
}

func TestResetAuthReplacesAllCredentials(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	claimPath := filepath.Join(dir, "claim-url")
	old, _, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"old-correct-horse-password",
	)
	if err != nil {
		t.Fatal(err)
	}

	next, claimURL, err := ResetAuth(
		statePath,
		"",
		"new-correct-horse-password",
		claimPath,
	)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccountID == old.AccountID || next.SessionKey == old.SessionKey {
		t.Fatal("reset preserved old account credentials")
	}
	if next.NodeID != old.NodeID {
		t.Fatal("reset changed the stable node ID")
	}
	if next.NodeName != "prod" {
		t.Fatalf("node name = %q", next.NodeName)
	}
	claimData, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(claimData)) != claimURL {
		t.Fatal("claim URL file does not match returned URL")
	}
	assertPrivateFile(t, claimPath)

	auth, err := LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Login(old.AccountID, "old-correct-horse-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old credentials error = %v", err)
	}
	if _, err := auth.Login(next.AccountID, "new-correct-horse-password"); err != nil {
		t.Fatalf("new credentials rejected: %v", err)
	}
	token := strings.TrimPrefix(claimURL, "https://rs.example.com/claim/")
	envelope, err := auth.Claim(token)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := protocol.OpenClaim(envelope, token)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Node.ID != old.NodeID || !strings.HasSuffix(bundle.Node.URL, "/r/"+next.AccountID) {
		t.Fatalf("claimed node = %+v", bundle.Node)
	}
}

func TestStateLockExcludesReset(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	if _, _, err := InitializeAuth(statePath, "https://rs.example.com", "prod", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := LockState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetAuth(statePath, "", "new-correct-horse-password", ""); err == nil {
		t.Fatal("reset ran while the state was locked")
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("locked state was modified")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResetAuth(statePath, "", "new-correct-horse-password", ""); err != nil {
		t.Fatalf("reset after unlock: %v", err)
	}
}

func TestSessionExpires(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	_, _, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := LoadAuthManager(statePath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.Login(auth.AccountID(), "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := auth.VerifySession(session.Token); err == nil {
		t.Fatal("expired session accepted")
	}
}

func TestClaimCanOnlyBeConsumedOnce(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	_, claimURL, err := InitializeAuth(
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
	token := strings.TrimPrefix(claimURL, "https://rs.example.com/claim/")

	var successes atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := auth.Claim(token); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrInvalidClaim) {
				t.Errorf("claim error = %v", err)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful claims = %d, want 1", successes.Load())
	}

	reloaded, err := LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Claim(token); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("persisted consumed claim error = %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state AuthState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.ClaimHash != "" || state.ClaimNonce != "" || state.ClaimCiphertext != "" {
		t.Fatal("consumed claim remains in persisted auth state")
	}
}

func TestAuthStateDoesNotContainClaimSecrets(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	_, claimURL, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(claimURL, "https://rs.example.com/claim/")
	for _, secret := range []string{"correct-horse-battery-staple", token} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("auth state contains plaintext secret %q", secret)
		}
	}
	var state AuthState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	envelope := protocol.ClaimEnvelope{
		Version:    protocol.ClaimVersion,
		AccountID:  state.AccountID,
		Nonce:      state.ClaimNonce,
		Ciphertext: state.ClaimCiphertext,
	}
	bundle, err := protocol.OpenClaim(envelope, token)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Node.Password != "correct-horse-battery-staple" {
		t.Fatal("claim did not preserve password")
	}
}

func TestInitializeAuthRefusesExistingState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "existing-auth.json")
	if err := os.WriteFile(statePath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	); err == nil {
		t.Fatal("existing auth state overwritten")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("existing auth state changed to %q", data)
	}
}

func TestInitializeAuthNoReplaceIsAtomic(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, _, err := InitializeAuth(
				statePath,
				"https://rs.example.com",
				"prod",
				"correct-horse-battery-staple",
			); err == nil {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful initializations = %d, want 1", successes.Load())
	}
	if _, err := LoadAuthManager(statePath, time.Minute); err != nil {
		t.Fatalf("persisted state is invalid: %v", err)
	}
}

func TestLoadAuthManagerRejectsMalformedSecrets(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	state, _, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*AuthState){
		"short password hash": func(value *AuthState) { value.PasswordHash = "AA" },
		"short session key":   func(value *AuthState) { value.SessionKey = "AA" },
		"incomplete claim":    func(value *AuthState) { value.ClaimNonce = "" },
		"zero epoch":          func(value *AuthState) { value.SessionEpoch = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := state
			mutate(&value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadAuthManager(statePath, time.Minute); err == nil {
				t.Fatal("malformed auth state was accepted")
			}
		})
	}
}

func TestLoadAuthManagerRejectsSubsecondTTL(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	if _, _, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthManager(statePath, 500*time.Millisecond); err == nil {
		t.Fatal("subsecond session TTL was accepted")
	}
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
	}
}
