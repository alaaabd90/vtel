package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// envelope.go wraps a compressed batch (see batch.go) in AES-256-GCM before
// it's uploaded, and reverses that on receive. This is a separate layer from
// batch.go on purpose - the batch format has no idea encryption exists, and
// stays that way; envelope.go only ever sees opaque bytes in and out.
//
// Unlike gdrive's protocol layer (which derives a *deterministic* nonce from
// a session ID + direction + monotonic sequence number, to avoid a
// crypto/rand call on every one of its much-higher-frequency small
// envelopes, and needs session-ID/restart-safety bookkeeping to keep that
// safe), vtel's batch frequency is capped by Telegram's own flood limits -
// nowhere near hot enough for a random nonce's rand.Read to matter - so a
// plain random nonce per envelope is simpler and correct by construction
// with no extra state.
const (
	envelopeMagic = "VTE2"
	keyLen        = 32 // AES-256
	nonceLen      = 12 // GCM standard nonce size
)

// DeriveKey derives the shared AES-256-GCM key every link in the pool uses,
// from Config.Secret. One shared secret across the whole pool is enough - no
// per-link subkeys, since that machinery in gdrive exists to key its much
// larger multi-lane pacing/quota system, not something vtel has.
func DeriveKey(secret string) ([]byte, error) {
	if secret == "" {
		return nil, errors.New("vtel: empty secret")
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:], nil
}

// SealEnvelope encrypts plaintext (a compressed batch) for upload:
// magic + random nonce + AES-256-GCM ciphertext (auth tag included by Seal).
// key must be keyLen bytes, as returned by DeriveKey.
func SealEnvelope(key []byte, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(envelopeMagic)+nonceLen+len(plaintext)+gcm.Overhead())
	out = append(out, envelopeMagic...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// OpenEnvelope reverses SealEnvelope. ok=false (with a nil error) means "not
// a vtel envelope" - bad magic, too short, or GCM authentication failure
// (wrong key, corrupt/tampered data) - so callers treat it as "skip", not a
// hard error.
func OpenEnvelope(key []byte, data []byte) (plaintext []byte, ok bool, err error) {
	if len(data) < len(envelopeMagic)+nonceLen {
		return nil, false, nil
	}
	if string(data[:len(envelopeMagic)]) != envelopeMagic {
		return nil, false, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, false, err
	}
	pos := len(envelopeMagic)
	nonce := data[pos : pos+nonceLen]
	pos += nonceLen
	ciphertext := data[pos:]
	plaintext, err = gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Wrong key or tampered/corrupt ciphertext - treat the same as "not
		// ours" rather than surfacing a crypto error to callers that just
		// want to know whether to process this document.
		return nil, false, nil
	}
	return plaintext, true, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keyLen {
		return nil, errors.New("vtel: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
