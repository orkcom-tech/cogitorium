package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// MinSecretKeyLen is the floor for COGITORIUM_SECRET_KEY. It is derived rather
// than used directly, so any string works — but a short one is guessable, and a
// guessable key over a database an operator already has means the encryption
// bought nothing.
const MinSecretKeyLen = 32

// sealedPrefix marks the stored form. It is a version marker, not decoration:
// changing the cipher or the derivation later has to be able to read what the
// old one wrote, and a row that says nothing about how it was made cannot be
// migrated without guessing.
const sealedPrefix = "v1:"

// keyInfo separates this key from any other use of the same material. HKDF's
// info string is what makes "the admin token, used as the secret key" not
// produce the same bytes in two places.
const keyInfo = "cogitorium secret store v1"

// Key seals and opens the secrets held in this install's own store.
//
// The material comes from the environment and from nowhere else — see
// config.Config.SecretKey for why it has no yaml tag. A nil *Key is the
// ordinary state of an install that has never set one: variables still work,
// a directory-mounted secret still works, and only writing a secret INTO the
// database is refused, because the alternative is writing it in plaintext.
type Key struct {
	aead cipher.AEAD
	id   string
}

// NewKey derives the encryption key from operator-supplied material.
func NewKey(material string) (*Key, error) {
	if len(material) < MinSecretKeyLen {
		return nil, fmt.Errorf("COGITORIUM_SECRET_KEY is %d characters; it encrypts every secret in the database, so at least %d are required",
			len(material), MinSecretKeyLen)
	}
	raw, err := hkdf.Key(sha256.New, []byte(material), nil, keyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("derive the secret key: %w", err)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("build the secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build the secret cipher: %w", err)
	}
	sum := sha256.Sum256(raw)
	return &Key{aead: aead, id: hex.EncodeToString(sum[:4])}, nil
}

// ID identifies the key without disclosing it, so a row can record which key
// sealed it and a mismatch can be reported as a mismatch.
func (k *Key) ID() string { return k.id }

// Seal encrypts one value for storage. The nonce is random per value and
// travels with the ciphertext: two rows holding the same password must not
// look the same in the database, or the database tells anyone who reads it
// which accounts share a password.
func (k *Key) Seal(plaintext string) (string, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("could not read random bytes to encrypt a secret: %w", err)
	}
	sealed := k.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return sealedPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts one stored value. keyID is what the row recorded, and is used
// only to tell the operator which of two very different things went wrong.
func (k *Key) Open(stored, keyID string) (string, error) {
	body, ok := strings.CutPrefix(stored, sealedPrefix)
	if !ok {
		return "", fmt.Errorf("this secret was not written by this build (no %q marker); it cannot be read and must be set again", sealedPrefix)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("this secret is not valid base64, so the row is damaged; set it again: %w", err)
	}
	if len(raw) < k.aead.NonceSize() {
		return "", errors.New("this secret is too short to be encrypted data, so the row is damaged; set it again")
	}
	nonce, body2 := raw[:k.aead.NonceSize()], raw[k.aead.NonceSize():]
	plain, err := k.aead.Open(nil, nonce, body2, nil)
	if err != nil {
		if keyID != "" && keyID != k.id {
			return "", fmt.Errorf("this secret was encrypted with a different COGITORIUM_SECRET_KEY (it wants key %s, this server has %s): "+
				"start the server with the original key, or set this secret again under the new one", keyID, k.id)
		}
		return "", errors.New("this secret could not be decrypted with COGITORIUM_SECRET_KEY; either the key changed or the row is damaged")
	}
	return string(plain), nil
}
