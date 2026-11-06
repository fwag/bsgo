package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func GeneratePAT(userID, name string, scopes []string) (string, *PersonalAccessToken, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", nil, fmt.Errorf("failed to generate random token: %w", err)
	}

	rawToken := "bsgo_pat_" + hex.EncodeToString(bytes)
	tokenHash := HashToken(rawToken)

	pat := &PersonalAccessToken{
		ID:        uuid.NewString(),
		UserID:    userID,
		Name:      name,
		TokenHash: tokenHash,
		Scopes:    scopes,
		CreatedAt: time.Now(),
	}

	return rawToken, pat, nil
}

func HashToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(h[:])
}

func ValidatePAT(rawToken, expectedHash string) bool {
	actualHash := HashToken(rawToken)
	return subtle.ConstantTimeCompare([]byte(actualHash), []byte(expectedHash)) == 1
}

func VerifyScope(scopes []string, action, resource, resourceID string) bool {
	for _, s := range scopes {
		if s == "*" || s == "*:*" || s == "*:*:*" {
			return true
		}

		parts := strings.SplitN(s, ":", 3)
		if matchScopeParts(parts, action, resource, resourceID) {
			return true
		}
	}
	return false
}

func matchScopeParts(parts []string, action, resource, resourceID string) bool {
	if len(parts) < 2 {
		return false
	}

	matchesActionResource := (matchPart(parts[0], action) && matchPart(parts[1], resource)) ||
		(matchPart(parts[0], resource) && matchPart(parts[1], action))
	if !matchesActionResource {
		return false
	}

	if len(parts) == 2 {
		return resourceID == "" || resourceID == "*"
	}
	return matchPart(parts[2], resourceID)
}

func matchPart(pattern, val string) bool {
	if pattern == "*" || pattern == val {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(val, prefix)
	}
	return false
}
