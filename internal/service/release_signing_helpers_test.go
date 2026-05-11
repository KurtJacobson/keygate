package service

import (
	"crypto/ed25519"
	"crypto/rand"
)

// ed25519Generate is a thin wrapper used by *_test.go files so the test
// imports stay focused on the helper layer rather than crypto/ed25519
// directly.
func ed25519Generate() ([]byte, []byte, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return pub, priv, nil
}

// ed25519Sign signs msg with priv and returns the raw 64-byte signature.
func ed25519Sign(priv, msg []byte) []byte {
	return ed25519.Sign(priv, msg)
}
