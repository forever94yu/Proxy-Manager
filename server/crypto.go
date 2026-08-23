package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type SecretBox struct {
	aead cipher.AEAD
}

func NewSecretBox(key []byte) (*SecretBox, error) {
	if len(key) != 32 {
		return nil, errors.New("AES-GCM master key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize AES: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize GCM: %w", err)
	}
	return &SecretBox{aead: aead}, nil
}

func (b *SecretBox) Encrypt(plaintext []byte, context string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate encryption nonce: %w", err)
	}
	sealed := b.aead.Seal(nil, nonce, plaintext, []byte(context))
	encoded := append(nonce, sealed...)
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func (b *SecretBox) Decrypt(encoded, context string) ([]byte, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("invalid encrypted secret encoding")
	}
	nonceSize := b.aead.NonceSize()
	if len(payload) < nonceSize+b.aead.Overhead() {
		return nil, errors.New("invalid encrypted secret length")
	}
	plaintext, err := b.aead.Open(nil, payload[:nonceSize], payload[nonceSize:], []byte(context))
	if err != nil {
		return nil, errors.New("encrypted secret authentication failed")
	}
	return plaintext, nil
}
