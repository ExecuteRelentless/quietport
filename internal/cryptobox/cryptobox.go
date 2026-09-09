// Package cryptobox: every piece of key handling in Quietport. No custom primitives (FR-50):
// x/crypto nacl/box (X25519 + XSalsa20-Poly1305, the WireGuard/NaCl family), chacha20poly1305, HKDF, ed25519.
package cryptobox

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/nacl/box"
)

var b64 = base64.StdEncoding

// RandomBytes returns n CSPRNG bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return b
}

// NewCircleSecret returns a 256-bit random value as base64 (FR-49). Used for both the crypt password and the salt.
func NewCircleSecret() string { return b64.EncodeToString(RandomBytes(32)) }

// NewInviteCode: 26 base32 chars = 130 bits of entropy (FR-101). Lowercase, no padding.
func NewInviteCode() string {
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(RandomBytes(17))[:26])
}

// NewToken returns a 256-bit random bearer token as hex.
func NewToken() string { return hex.EncodeToString(RandomBytes(32)) }

// HashToken is what the hub stores for invite codes and bearer tokens.
func HashToken(s string) string {
	h := sha256.Sum256([]byte("quietport-token-v1:" + s))
	return hex.EncodeToString(h[:])
}

// --- invite payload encryption (code-derived key; the hub only has HashToken(code)) ---

func inviteKey(code string) []byte {
	k, err := hkdf.Key(sha256.New, []byte(code), []byte("quietport-invite-v1"), "circle-keys", chacha20poly1305.KeySize)
	if err != nil {
		panic(err)
	}
	return k
}

// SealWithCode encrypts v (JSON) under a key derived from the invite code.
func SealWithCode(code string, v any) (string, error) {
	pt, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.NewX(inviteKey(code))
	if err != nil {
		return "", err
	}
	nonce := RandomBytes(aead.NonceSize())
	ct := aead.Seal(nonce, nonce, pt, nil)
	return b64.EncodeToString(ct), nil
}

// OpenWithCode reverses SealWithCode into v.
func OpenWithCode(code, sealed string, v any) error {
	ct, err := b64.DecodeString(sealed)
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(inviteKey(code))
	if err != nil {
		return err
	}
	if len(ct) < aead.NonceSize() {
		return errors.New("sealed payload too short")
	}
	pt, err := aead.Open(nil, ct[:aead.NonceSize()], ct[aead.NonceSize():], nil)
	if err != nil {
		return errors.New("invite code does not open this payload")
	}
	return json.Unmarshal(pt, v)
}

// --- device keypairs and sealed key grants ---

type DeviceKeys struct {
	Public  string `json:"public"`  // base64
	Private string `json:"private"` // base64
}

func NewDeviceKeys() (DeviceKeys, error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return DeviceKeys{}, err
	}
	return DeviceKeys{Public: b64.EncodeToString(pub[:]), Private: b64.EncodeToString(priv[:])}, nil
}

// SealToDevice encrypts v (JSON) so that only the holder of the device private key can read it.
func SealToDevice(devicePub string, v any) (string, error) {
	pk, err := key32(devicePub)
	if err != nil {
		return "", err
	}
	pt, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	ct, err := box.SealAnonymous(nil, pt, pk, rand.Reader)
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(ct), nil
}

// OpenFromDevice decrypts a SealToDevice box with the device keypair.
func OpenFromDevice(keys DeviceKeys, sealed string, v any) error {
	pk, err := key32(keys.Public)
	if err != nil {
		return err
	}
	sk, err := key32(keys.Private)
	if err != nil {
		return err
	}
	ct, err := b64.DecodeString(sealed)
	if err != nil {
		return err
	}
	pt, ok := box.OpenAnonymous(nil, ct, pk, sk)
	if !ok {
		return errors.New("sealed grant does not open with this device key")
	}
	return json.Unmarshal(pt, v)
}

func key32(s string) (*[32]byte, error) {
	b, err := b64.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("bad 32-byte key")
	}
	var k [32]byte
	copy(k[:], b)
	return &k, nil
}

// --- symmetric file encryption for the operator keystore and key export bundles ---

// EncryptWithKey: XChaCha20-Poly1305 with a random nonce, key is 32 bytes.
func EncryptWithKey(key, pt []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := RandomBytes(aead.NonceSize())
	return aead.Seal(nonce, nonce, pt, nil), nil
}

func DecryptWithKey(key, ct []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(ct) < aead.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return aead.Open(nil, ct[:aead.NonceSize()], ct[aead.NonceSize():], nil)
}

// PassphraseKey derives a 32-byte key from a passphrase + salt for export bundles.
func PassphraseKey(pass string, salt []byte) []byte {
	k, err := hkdf.Key(sha256.New, []byte(pass), salt, "quietport-export-v1", 32)
	if err != nil {
		panic(err)
	}
	return k
}

// --- release signing (NFR-40) ---

func NewSigningKey() (pub, priv string) {
	p, s, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return b64.EncodeToString(p), b64.EncodeToString(s)
}

func Sign(privB64 string, msg []byte) (string, error) {
	sk, err := b64.DecodeString(privB64)
	if err != nil || len(sk) != ed25519.PrivateKeySize {
		return "", errors.New("bad signing key")
	}
	return b64.EncodeToString(ed25519.Sign(ed25519.PrivateKey(sk), msg)), nil
}

func Verify(pubB64 string, msg []byte, sigB64 string) bool {
	pk, err := b64.DecodeString(pubB64)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return false
	}
	sig, err := b64.DecodeString(sigB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pk), msg, sig)
}

// SHA256Hex of a byte slice.
func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
