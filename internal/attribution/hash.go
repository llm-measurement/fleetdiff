// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package attribution shares local key handling and source-separated identities.
package attribution

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

func Secret(env string) ([]byte, string, error) {
	if env == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, "", errors.New("cannot generate a local hashing secret")
		}
		return b, "ephemeral", nil
	}
	if len(env) > 128 || len(os.Getenv(env)) > 4096 {
		return nil, "", errors.New("invalid hash secret environment setting")
	}
	if _, err := sketchhash.SecretFromEnv(env); err != nil {
		return nil, "", errors.New("hash secret missing or weak; use a random secret of at least 16 bytes")
	}
	return []byte(os.Getenv(env)), "environment", nil
}

// Identity preserves the established source/kind/component framing and text_v1.
func Identity(key []byte, source, kind string, parts ...string) ([32]byte, error) {
	canonical := []string{source, kind}
	for _, part := range parts {
		b, err := canon.CanonicalizeString(canon.TextV1, part)
		if err != nil {
			return [32]byte{}, errors.New("identity is not valid UTF-8")
		}
		canonical = append(canonical, string(b))
	}
	b, _ := json.Marshal(canonical)
	return Digest(key, string(sketchhash.UserV1), b), nil
}

func Digest(key []byte, domain string, data []byte) [32]byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(domain))
	mac.Write([]byte{0})
	mac.Write(data)
	return [32]byte(mac.Sum(nil))
}
