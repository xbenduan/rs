package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"atlas/rs/internal/fsx"
	"atlas/rs/internal/protocol"
)

const authStateVersion = 1

var (
	ErrInvalidCredentials = errors.New("invalid account or password")
	ErrInvalidClaim       = errors.New("invalid or consumed claim URL")
)

// AuthState is the server-side authentication state persisted with mode 0600.
type AuthState struct {
	Version         int    `json:"version"`
	Endpoint        string `json:"endpoint"`
	NodeName        string `json:"node_name"`
	NodeID          string `json:"node_id"`
	AccountID       string `json:"account_id"`
	PasswordHash    string `json:"password_hash"`
	SessionKey      string `json:"session_key"`
	SessionEpoch    uint64 `json:"session_epoch"`
	ClaimHash       string `json:"claim_hash,omitempty"`
	ClaimNonce      string `json:"claim_nonce,omitempty"`
	ClaimCiphertext string `json:"claim_ciphertext,omitempty"`
}

type sessionClaims struct {
	AccountID string `json:"account_id"`
	Epoch     uint64 `json:"epoch"`
	Expires   int64  `json:"expires"`
	Nonce     string `json:"nonce"`
}

// AuthManager owns password login and short-lived sessions.
type AuthManager struct {
	mu         sync.RWMutex
	state      AuthState
	stateFile  string
	sessionTTL time.Duration
}

// InitializeAuth creates authentication state and an encrypted one-time claim.
func InitializeAuth(stateFile, endpoint, nodeName, password string) (AuthState, string, error) {
	if _, err := os.Stat(stateFile); err == nil {
		return AuthState{}, "", fmt.Errorf("refusing to overwrite existing file: %s", stateFile)
	} else if !errors.Is(err, os.ErrNotExist) {
		return AuthState{}, "", err
	}

	state, claimURL, err := newAuthState(endpoint, nodeName, password, "")
	if err != nil {
		return AuthState{}, "", err
	}
	if err := writeState(stateFile, state, false); err != nil {
		return AuthState{}, "", err
	}
	return state, claimURL, nil
}

// ResetAuth replaces all credentials from the server's local state file. It
// keeps only the endpoint, node name and node ID of the current state. It
// fails while rs serve holds the state.
func ResetAuth(stateFile, nodeName, password, claimURLFile string) (AuthState, string, error) {
	unlock, err := LockState(stateFile)
	if err != nil {
		return AuthState{}, "", err
	}
	defer unlock()

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return AuthState{}, "", fmt.Errorf("read auth state: %w", err)
	}
	var current struct {
		Endpoint string `json:"endpoint"`
		NodeName string `json:"node_name"`
		NodeID   string `json:"node_id"`
	}
	if err := json.Unmarshal(data, &current); err != nil {
		return AuthState{}, "", fmt.Errorf("parse auth state: %w", err)
	}
	if nodeName == "" {
		nodeName = current.NodeName
	}
	if nodeName == "" {
		return AuthState{}, "", errors.New("auth state has no node name; pass --name")
	}
	state, claimURL, err := newAuthState(current.Endpoint, nodeName, password, current.NodeID)
	if err != nil {
		return AuthState{}, "", err
	}
	if claimURLFile != "" {
		if err := fsx.WriteFileAtomic(claimURLFile, []byte(claimURL+"\n"), 0o600, false); err != nil {
			return AuthState{}, "", fmt.Errorf("write claim URL: %w", err)
		}
	}
	if err := writeState(stateFile, state, true); err != nil {
		if claimURLFile != "" {
			_ = os.Remove(claimURLFile)
		}
		return AuthState{}, "", err
	}
	return state, claimURL, nil
}

func newAuthState(endpoint, nodeName, password, nodeID string) (AuthState, string, error) {
	if err := validatePassword(password); err != nil {
		return AuthState{}, "", err
	}
	if err := protocol.ValidateNodeName(nodeName); err != nil {
		return AuthState{}, "", err
	}
	publicURL, err := url.Parse(endpoint)
	if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" || publicURL.User != nil ||
		(publicURL.Path != "" && publicURL.Path != "/") || publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return AuthState{}, "", errors.New("endpoint must be an HTTPS origin, e.g. https://rs.example.com")
	}

	accountID, err := randomString(16)
	if err != nil {
		return AuthState{}, "", err
	}
	sessionKey, err := randomString(32)
	if err != nil {
		return AuthState{}, "", err
	}
	claimToken, err := randomString(32)
	if err != nil {
		return AuthState{}, "", err
	}
	if nodeID == "" {
		nodeID, err = randomString(16)
		if err != nil {
			return AuthState{}, "", err
		}
	} else {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(nodeID)
		if decodeErr != nil || len(decoded) != 16 {
			return AuthState{}, "", errors.New("invalid node ID")
		}
	}
	hash, err := hashPassword(password)
	if err != nil {
		return AuthState{}, "", err
	}

	endpoint = strings.TrimRight(publicURL.String(), "/")
	bundle := protocol.ClaimBundle{
		Version: protocol.ClaimVersion,
		Node: protocol.ClaimNode{
			Name:     nodeName,
			ID:       nodeID,
			URL:      endpoint + "/r/" + accountID,
			Password: password,
		},
	}
	claimNonce, claimCiphertext, err := protocol.SealClaim(bundle, claimToken, accountID)
	if err != nil {
		return AuthState{}, "", err
	}
	state := AuthState{
		Version:         authStateVersion,
		Endpoint:        endpoint,
		NodeName:        nodeName,
		NodeID:          nodeID,
		AccountID:       accountID,
		PasswordHash:    hash,
		SessionKey:      sessionKey,
		SessionEpoch:    1,
		ClaimHash:       hashClaim(claimToken),
		ClaimNonce:      claimNonce,
		ClaimCiphertext: claimCiphertext,
	}
	return state, endpoint + "/claim/" + claimToken, nil
}

// LoadAuthManager loads authentication state for a running server.
func LoadAuthManager(stateFile string, sessionTTL time.Duration) (*AuthManager, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, fmt.Errorf("read auth state: %w", err)
	}
	var state AuthState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse auth state: %w", err)
	}
	if err := validateAuthState(state); err != nil {
		return nil, err
	}
	if sessionTTL <= 0 {
		sessionTTL = 15 * time.Minute
	} else if sessionTTL < time.Second {
		return nil, errors.New("session TTL must be at least one second")
	}
	return &AuthManager{
		state:      state,
		stateFile:  stateFile,
		sessionTTL: sessionTTL,
	}, nil
}

// AccountID returns the public account identifier.
func (a *AuthManager) AccountID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state.AccountID
}

// Claim consumes the URL and returns only the client-decryptable envelope.
func (a *AuthManager) Claim(token string) (protocol.ClaimEnvelope, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	state := a.state
	if state.ClaimHash == "" ||
		subtle.ConstantTimeCompare([]byte(hashClaim(token)), []byte(state.ClaimHash)) != 1 {
		return protocol.ClaimEnvelope{}, ErrInvalidClaim
	}

	next := state
	next.ClaimHash = ""
	next.ClaimNonce = ""
	next.ClaimCiphertext = ""
	if err := writeState(a.stateFile, next, true); err != nil {
		return protocol.ClaimEnvelope{}, err
	}
	a.state = next
	return protocol.ClaimEnvelope{
		Version:    protocol.ClaimVersion,
		AccountID:  state.AccountID,
		Nonce:      state.ClaimNonce,
		Ciphertext: state.ClaimCiphertext,
	}, nil
}

// Login validates a password and returns a short-lived signed session.
func (a *AuthManager) Login(accountID, password string) (protocol.SessionResponse, error) {
	a.mu.RLock()
	state := a.state
	a.mu.RUnlock()

	accountMatches := subtle.ConstantTimeCompare([]byte(accountID), []byte(state.AccountID)) == 1
	passwordMatches := verifyPassword(password, state.PasswordHash)
	if !accountMatches || !passwordMatches {
		return protocol.SessionResponse{}, ErrInvalidCredentials
	}

	now := time.Now()
	expires := now.Add(a.sessionTTL)
	nonce, err := randomString(12)
	if err != nil {
		return protocol.SessionResponse{}, err
	}
	claims := sessionClaims{
		AccountID: state.AccountID,
		Epoch:     state.SessionEpoch,
		Expires:   expires.Unix(),
		Nonce:     nonce,
	}
	token, err := signClaims(claims, state.SessionKey)
	if err != nil {
		return protocol.SessionResponse{}, err
	}
	return protocol.SessionResponse{
		Token:     token,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
		NodeID:    state.NodeID,
		Version:   Version,
	}, nil
}

// VerifySession validates a signed session against the current credential epoch.
func (a *AuthManager) VerifySession(token string) error {
	a.mu.RLock()
	state := a.state
	a.mu.RUnlock()

	claims, err := verifyClaims(token, state.SessionKey)
	if err != nil {
		return err
	}
	if claims.AccountID != state.AccountID || claims.Epoch != state.SessionEpoch || time.Now().Unix() >= claims.Expires {
		return errors.New("expired or revoked session")
	}
	return nil
}

func validateAuthState(state AuthState) error {
	if state.Version != authStateVersion || state.SessionEpoch == 0 {
		return errors.New("invalid auth state")
	}
	endpoint, err := url.Parse(state.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("invalid auth state: endpoint")
	}
	accountID, err := base64.RawURLEncoding.DecodeString(state.AccountID)
	if err != nil || len(accountID) != 16 {
		return errors.New("invalid auth state: account ID")
	}
	if err := protocol.ValidateNodeName(state.NodeName); err != nil {
		return fmt.Errorf("invalid auth state: %w", err)
	}
	nodeID, err := base64.RawURLEncoding.DecodeString(state.NodeID)
	if err != nil || len(nodeID) != 16 {
		return errors.New("invalid auth state: node ID")
	}
	for name, encoded := range map[string]string{
		"password hash": state.PasswordHash,
		"session key":   state.SessionKey,
	} {
		value, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(value) != 32 {
			return fmt.Errorf("invalid auth state: %s", name)
		}
	}
	claimFields := []string{state.ClaimHash, state.ClaimNonce, state.ClaimCiphertext}
	present := 0
	for _, field := range claimFields {
		if field != "" {
			present++
		}
	}
	if present != 0 && present != len(claimFields) {
		return errors.New("invalid auth state: incomplete claim")
	}
	if present != 0 {
		hash, hashErr := base64.RawURLEncoding.DecodeString(state.ClaimHash)
		nonce, nonceErr := base64.RawURLEncoding.DecodeString(state.ClaimNonce)
		ciphertext, ciphertextErr := base64.RawURLEncoding.DecodeString(state.ClaimCiphertext)
		if hashErr != nil || len(hash) != sha256.Size || nonceErr != nil || len(nonce) != 12 ||
			ciphertextErr != nil || len(ciphertext) < 16 {
			return errors.New("invalid auth state: malformed claim")
		}
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < 12 {
		return errors.New("password must contain at least 12 bytes")
	}
	if len(password) > 1024 {
		return errors.New("password is too long")
	}
	return nil
}

// hashPassword hashes a credential with SHA-256. rs credentials are always
// high-entropy machine-generated secrets (never human-chosen), so a slow,
// salted KDF buys nothing: there is no low-entropy input to protect and no
// cross-account reuse for a salt to defend against.
func hashPassword(password string) (string, error) {
	sum := sha256.Sum256([]byte(password))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func verifyPassword(password, encodedHash string) bool {
	expected, err := base64.RawURLEncoding.DecodeString(encodedHash)
	if err != nil {
		return false
	}
	actual := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(actual[:], expected) == 1
}

func hashClaim(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func signClaims(claims sessionClaims, encodedKey string) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	key, err := base64.RawURLEncoding.DecodeString(encodedKey)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyClaims(token, encodedKey string) (sessionClaims, error) {
	var claims sessionClaims
	body, signature, ok := strings.Cut(token, ".")
	if !ok {
		return claims, errors.New("invalid session")
	}
	key, err := base64.RawURLEncoding.DecodeString(encodedKey)
	if err != nil {
		return claims, errors.New("invalid session")
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return claims, errors.New("invalid session")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(body))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return claims, errors.New("invalid session")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return claims, errors.New("invalid session")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, errors.New("invalid session")
	}
	return claims, nil
}

func randomString(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func writeState(path string, state AuthState, replace bool) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, append(data, '\n'), 0o600, replace)
}
