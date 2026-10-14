// Package protocol defines the shared HTTP authentication protocol.
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// ClaimVersion is the current one-time credential bundle format.
const ClaimVersion = 1

// ClaimNode is the client node entry delivered by a one-time claim URL.
type ClaimNode struct {
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	IP       string `json:"ip,omitempty"`
	URL      string `json:"url"`
	Password string `json:"password"`
}

// ClaimBundle contains the login credentials encrypted for one-time delivery.
type ClaimBundle struct {
	Version int       `json:"version"`
	Node    ClaimNode `json:"node"`
}

// ClaimEnvelope is returned publicly; only the URL token can open it.
type ClaimEnvelope struct {
	Version    int    `json:"version"`
	AccountID  string `json:"account_id"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// SessionResponse is returned after a successful password login.
type SessionResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	NodeID    string `json:"node_id"`
	Version   string `json:"version"`
}

// SealClaim encrypts a one-time credential bundle for delivery.
func SealClaim(bundle ClaimBundle, token, accountID string) (string, string, error) {
	plaintext, err := json.Marshal(bundle)
	if err != nil {
		return "", "", err
	}
	aead, err := claimCipher(token)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, []byte(accountID))
	return base64.RawURLEncoding.EncodeToString(nonce), base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// OpenClaim decrypts and validates a one-time credential bundle.
func OpenClaim(envelope ClaimEnvelope, token string) (ClaimBundle, error) {
	var bundle ClaimBundle
	aead, err := claimCipher(token)
	if err != nil {
		return bundle, err
	}
	if envelope.Version != ClaimVersion || envelope.AccountID == "" {
		return bundle, errors.New("invalid claim envelope")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return bundle, errors.New("invalid claim nonce")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return bundle, errors.New("invalid claim ciphertext")
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(envelope.AccountID))
	if err != nil {
		return bundle, err
	}
	if err := json.Unmarshal(plaintext, &bundle); err != nil {
		return bundle, err
	}
	if bundle.Version != ClaimVersion || bundle.Node.URL == "" || bundle.Node.Password == "" {
		return ClaimBundle{}, errors.New("invalid claim bundle")
	}
	return bundle, nil
}

func claimCipher(token string) (cipher.AEAD, error) {
	key, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid claim token")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ValidateNodeName enforces the node name alphabet shared by server state and
// the client node list.
func ValidateNodeName(name string) error {
	if name == "" || len(name) > 64 {
		return errors.New("node name must contain 1 to 64 characters")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' {
			return errors.New("node name may contain only letters, digits, dot, underscore and hyphen")
		}
	}
	return nil
}
