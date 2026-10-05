// Package secrets holds the instance key, symmetric encryption of channel
// configs, HMAC-signed one-click tokens, argon2id hashing for passwords
// and API keys, and API key generation. It depends on nothing internal.
package secrets

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// Keyring is the 32-byte instance key.
type Keyring struct {
	key []byte
}

// Load reads the key file, creating it with 32 random bytes and mode 0600
// when it does not exist.
func Load(path string) (*Keyring, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw = make([]byte, chacha20poly1305.KeySize)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create key directory: %w", err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, fmt.Errorf("write key file: %w", err)
		}
		return &Keyring{key: raw}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	if len(raw) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("key file %s must hold exactly %d bytes, has %d", path, chacha20poly1305.KeySize, len(raw))
	}
	return &Keyring{key: raw}, nil
}

// FromBytes wraps an existing key, for tests.
func FromBytes(key []byte) (*Keyring, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, errors.New("key must be 32 bytes")
	}
	return &Keyring{key: append([]byte(nil), key...)}, nil
}

// Seal encrypts plain with XChaCha20-Poly1305 and returns "v1:" + base64.
func (k *Keyring) Seal(plain []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(k.key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, plain, nil)
	return "v1:" + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal.
func (k *Keyring) Open(sealed string) ([]byte, error) {
	b64, ok := strings.CutPrefix(sealed, "v1:")
	if !ok {
		return nil, errors.New("unknown sealed format")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(k.key)
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("sealed value too short")
	}
	return aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
}

// Sign returns a URL-safe token binding purpose and payload until exp.
func (k *Keyring) Sign(purpose, payload string, exp time.Time) string {
	body := purpose + "|" + payload + "|" + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, k.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a token for purpose and returns its payload.
func (k *Keyring) Verify(purpose, token string, now time.Time) (string, error) {
	bodyB64, sigB64, ok := strings.Cut(token, ".")
	if !ok {
		return "", errors.New("malformed token")
	}
	body, err := base64.RawURLEncoding.DecodeString(bodyB64)
	if err != nil {
		return "", errors.New("malformed token")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return "", errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, k.key)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", errors.New("bad signature")
	}
	parts := strings.SplitN(string(body), "|", 3)
	if len(parts) != 3 || parts[0] != purpose {
		return "", errors.New("token purpose mismatch")
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.Unix() > exp {
		return "", errors.New("token expired")
	}
	return parts[1], nil
}

// argon2id parameters from the design document: t=2, m=64MB, p=1.
// They are variables only so tests can shrink them; see FastParamsForTests.
var (
	argonTime    uint32 = 2
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 1
)

const (
	argonKeyLen = 32
	saltLen     = 16
)

// FastParamsForTests lowers the argon2 cost so test suites that create
// many users and keys stay quick. Hashes made with it are still valid
// PHC strings; VerifyPassword reads the parameters from the hash.
func FastParamsForTests() {
	argonTime, argonMemory, argonThreads = 1, 8*1024, 1
}

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC hash. It takes
// the same time whether or not the hash exists, so callers can pass an
// empty hash for unknown users.
func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		// Burn the same work as a real comparison.
		argon2.IDKey([]byte(password), make([]byte, saltLen), argonTime, argonMemory, argonThreads, argonKeyLen)
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// HashToken hashes an API key or similar high-entropy token. The salt is
// derived from the token so verification can start from the prefix
// lookup, and the work factor is the same as for passwords.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte("vink-token-salt:" + token))
	key := argon2.IDKey([]byte(token), sum[:saltLen], argonTime, argonMemory, argonThreads, argonKeyLen)
	return "argon2id:" + base64.RawStdEncoding.EncodeToString(key)
}

// VerifyToken reports whether token matches a HashToken value.
func VerifyToken(hash, token string) bool {
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(hash)) == 1
}

const keyAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// APIKeyPrefixLen is the visible part of an API key.
const APIKeyPrefixLen = 8

// NewAPIKey returns a plaintext key "vk_<prefix>_<secret>" and its prefix.
func NewAPIKey() (token, prefix string, err error) {
	prefix, err = randomString(APIKeyPrefixLen)
	if err != nil {
		return "", "", err
	}
	secret, err := randomString(32)
	if err != nil {
		return "", "", err
	}
	return "vk_" + prefix + "_" + secret, prefix, nil
}

// NewAgentToken returns a plaintext agent token "vat_<prefix>_<secret>"
// and its prefix; it is hashed like an API key.
func NewAgentToken() (token, prefix string, err error) {
	prefix, err = randomString(APIKeyPrefixLen)
	if err != nil {
		return "", "", err
	}
	secret, err := randomString(32)
	if err != nil {
		return "", "", err
	}
	return "vat_" + prefix + "_" + secret, prefix, nil
}

// NewAdminKey returns a plaintext instance admin key "vka_<prefix>_<secret>"
// and its prefix; it is hashed like an API key. The vka_ prefix picks the
// verify path, so it never reaches project or org keys.
func NewAdminKey() (token, prefix string, err error) {
	prefix, err = randomString(APIKeyPrefixLen)
	if err != nil {
		return "", "", err
	}
	secret, err := randomString(32)
	if err != nil {
		return "", "", err
	}
	return "vka_" + prefix + "_" + secret, prefix, nil
}

// ParseAdminKeyPrefix returns the prefix of a well-formed admin key.
func ParseAdminKeyPrefix(token string) (string, bool) {
	rest, ok := strings.CutPrefix(token, "vka_")
	if !ok {
		return "", false
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok || len(prefix) != APIKeyPrefixLen || len(secret) != 32 {
		return "", false
	}
	return prefix, true
}

// ParseAgentTokenPrefix returns the prefix of a well-formed agent token.
func ParseAgentTokenPrefix(token string) (string, bool) {
	rest, ok := strings.CutPrefix(token, "vat_")
	if !ok {
		return "", false
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok || len(prefix) != APIKeyPrefixLen || len(secret) != 32 {
		return "", false
	}
	return prefix, true
}

// ParseAPIKeyPrefix returns the prefix of a well-formed key.
func ParseAPIKeyPrefix(token string) (string, bool) {
	rest, ok := strings.CutPrefix(token, "vk_")
	if !ok || len(rest) < APIKeyPrefixLen+1+32 || rest[APIKeyPrefixLen] != '_' {
		return "", false
	}
	return rest[:APIKeyPrefixLen], true
}

// RandomToken returns n bytes of randomness, base64url encoded.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Fingerprint is a stable short hash for caches and logs.
func Fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = keyAlphabet[int(v)%len(keyAlphabet)]
	}
	return string(out), nil
}

// NewLinkToken makes a one-time link token with the given prefix (iv_ for
// invites, rs_ for reset links): 256 bits of randomness, so SHA-256 is
// the right hash for it and the lookup is direct.
func NewLinkToken(prefix string) (string, error) {
	s, err := randomString(43)
	if err != nil {
		return "", err
	}
	return prefix + s, nil
}

// HashLink is the stored form of a one-time link token.
func HashLink(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
