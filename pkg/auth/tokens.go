package auth

import (
	"crypto/rand"
	"crypto/sha256"
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
	return HashToken(rawToken) == expectedHash
}

func VerifyScope(scopes []string, action, resource, resourceID string) bool {
	for _, s := range scopes {
		if s == "*" || s == "*:*" || s == "*:*:*" {
			return true
		}

		parts := strings.SplitN(s, ":", 3)
		if len(parts) == 2 {
			if (matchPart(parts[0], action) && matchPart(parts[1], resource)) ||
				(matchPart(parts[0], resource) && matchPart(parts[1], action)) {
				return true
			}
		} else if len(parts) == 3 {
			matchesActionResource := (matchPart(parts[0], action) && matchPart(parts[1], resource)) ||
				(matchPart(parts[0], resource) && matchPart(parts[1], action))
			if matchesActionResource && matchPart(parts[2], resourceID) {
				return true
			}
		}
	}
	return false
}

func matchPart(pattern, val string) bool {
	return pattern == "*" || pattern == val
}
