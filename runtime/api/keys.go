package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// KeyManager handles API key issuance and verification. Only an argon2id hash
// of the key is stored — the raw value is shown once at creation.
type KeyManager struct {
	store *sqlite.Store
	// adminKey is a static key from config (used by CLI) with all scopes.
	adminKey string
}

func NewKeyManager(store *sqlite.Store, adminKey string) *KeyManager {
	return &KeyManager{store: store, adminKey: adminKey}
}

// Create issues a new scoped key and returns the raw value exactly once.
func (km *KeyManager) Create(name string, scopes []string) (raw, id string, err error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = "wft_live_" + base64.RawURLEncoding.EncodeToString(buf)
	hash, err := hashKey(raw)
	if err != nil {
		return "", "", err
	}
	id = "key_" + raw[9:17] // short id
	k := sqlite.APIKey{
		ID:        id,
		Name:      name,
		KeyHash:   hash,
		Scopes:    scopes,
		CreatedAt: time.Now().UTC(),
	}
	if err := km.store.SaveAPIKey(context.Background(), k); err != nil {
		return "", "", err
	}
	return raw, id, nil
}

// Verify checks a bearer token and returns its record (scopes, expiry).
func (km *KeyManager) Verify(token string) (sqlite.APIKey, error) {
	// admin key from config has all scopes
	if km.adminKey != "" && token == km.adminKey {
		return sqlite.APIKey{
			ID:     "admin",
			Name:   "admin",
			Scopes: []string{"jobs:read", "jobs:write", "queue:read", "workers:read", "workers:write", "storage:manage", "webhooks:manage", "profiles:read", "plugins:read", "metrics:read", "keys:manage"},
		}, nil
	}
	keys, err := km.store.ListAPIKeys(context.Background())
	if err != nil {
		return sqlite.APIKey{}, err
	}
	for _, k := range keys {
		if k.RevokedAt != nil {
			continue
		}
		if k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()) {
			continue
		}
		ok, err := verifyKey(token, k.KeyHash)
		if err != nil {
			continue
		}
		if ok {
			return k, nil
		}
	}
	return sqlite.APIKey{}, errors.New("key not found")
}

// hashKey derives an argon2id hash in the format: $argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>
func hashKey(raw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(raw), salt, 1, 64*1024, 4, 32)
	return "$argon2id$v=19$m=65536,t=1,p=4$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(hash), nil
}

func verifyKey(raw, stored string) (bool, error) {
	parts := splitParts(stored)
	if len(parts) != 6 {
		return false, errors.New("malformed hash")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	expect, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	hash := argon2.IDKey([]byte(raw), salt, 1, 64*1024, 4, uint32(len(expect)))
	return constantTimeEqual(hash, expect), nil
}

func splitParts(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '$' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
