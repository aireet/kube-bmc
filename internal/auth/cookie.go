package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// cookieCodec encrypts and authenticates cookie payloads with AES-256-GCM.
type cookieCodec struct{ aead cipher.AEAD }

func newCookieCodec(secret []byte) (cookieCodec, error) {
	if len(secret) < 32 {
		return cookieCodec{}, errors.New("session secret must be at least 32 bytes")
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return cookieCodec{}, err
	}
	aead, err := cipher.NewGCM(block)
	return cookieCodec{aead: aead}, err
}

type envelope struct {
	Expires int64           `json:"exp"`
	Data    json.RawMessage `json:"d"`
}

// encode seals v with an expiry. name is bound as additional data so a value cannot be
// replayed under a different cookie name.
func (c cookieCodec) encode(name string, v any, ttl time.Duration) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(envelope{Expires: time.Now().Add(ttl).Unix(), Data: data})
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(c.aead.Seal(nonce, nonce, plain, []byte(name))), nil
}

func (c cookieCodec) decode(name, value string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < c.aead.NonceSize() {
		return errors.New("malformed cookie")
	}
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, raw[:n], raw[n:], []byte(name))
	if err != nil {
		return errors.New("invalid cookie")
	}
	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return err
	}
	if time.Now().Unix() > env.Expires {
		return errors.New("cookie expired")
	}
	return json.Unmarshal(env.Data, v)
}
