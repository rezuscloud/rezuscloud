package talosconfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// SecretsStore encrypts and stores Talos secrets bundles.
type SecretsStore struct {
	mu   sync.Mutex
	key  []byte
	path string
}

// NewSecretsStore creates or loads a secrets store.
// The encryption key is loaded from keyPath, or generated if not present.
func NewSecretsStore(dataPath, keyPath string) (*SecretsStore, error) {
	key, err := loadOrGenerateKey(keyPath)
	if err != nil {
		return nil, fmt.Errorf("load key: %w", err)
	}

	s := &SecretsStore{
		key:  key,
		path: dataPath,
	}

	return s, nil
}

// SecretsData holds all tenant secrets bundles.
type SecretsData struct {
	Tenants map[string]json.RawMessage `json:"tenants"`
}

// Load loads secrets from disk.
func (s *SecretsStore) Load() (*SecretsData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &SecretsData{Tenants: make(map[string]json.RawMessage)}, nil
		}
		return nil, err
	}

	decrypted, err := s.decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	var sd SecretsData
	if err := json.Unmarshal(decrypted, &sd); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	if sd.Tenants == nil {
		sd.Tenants = make(map[string]json.RawMessage)
	}

	return &sd, nil
}

// Save persists secrets to disk (encrypted).
func (s *SecretsStore) Save(data *SecretsData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	encrypted, err := s.encrypt(raw)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	return os.WriteFile(s.path, encrypted, 0o600)
}

// StoreTenantBundle encrypts and stores a tenant's secrets bundle.
func (s *SecretsStore) StoreTenantBundle(tenant string, bundle json.RawMessage) error {
	data, err := s.Load()
	if err != nil {
		return err
	}
	data.Tenants[tenant] = bundle
	return s.Save(data)
}

// LoadTenantBundle loads a tenant's secrets bundle.
func (s *SecretsStore) LoadTenantBundle(tenant string) (json.RawMessage, error) {
	data, err := s.Load()
	if err != nil {
		return nil, err
	}
	bundle, ok := data.Tenants[tenant]
	if !ok {
		return nil, nil
	}
	return bundle, nil
}

// DeleteTenantBundle removes a tenant's secrets bundle.
func (s *SecretsStore) DeleteTenantBundle(tenant string) error {
	data, err := s.Load()
	if err != nil {
		return err
	}
	delete(data.Tenants, tenant)
	return s.Save(data)
}

func (s *SecretsStore) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *SecretsStore) decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func loadOrGenerateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil && len(key) == 32 {
		return key, nil
	}

	// Generate new key.
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}

	return key, nil
}
