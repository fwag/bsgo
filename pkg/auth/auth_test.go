package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openSUSE/bsgo/pkg/auth"
)

func TestTokenScopesBlastRadius(t *testing.T) {
	scopes := []string{
		"project:write:home:ai-agent",
		"build:trigger:*",
		"project:read:*",
	}

	// 1. Should ALLOW writing to assigned personal project
	if !auth.VerifyScope(scopes, "write", "project", "home:ai-agent") {
		t.Errorf("expected ALLOW for write project home:ai-agent")
	}

	// 2. MUST DENY writing to production distribution repository (blast-radius containment!)
	if auth.VerifyScope(scopes, "write", "project", "openSUSE:Factory") {
		t.Errorf("expected DENY for write project openSUSE:Factory with agent token")
	}

	// 3. Should ALLOW trigger on any project due to wildcard
	if !auth.VerifyScope(scopes, "trigger", "build", "openSUSE:Factory") {
		t.Errorf("expected ALLOW for build trigger openSUSE:Factory with wildcard")
	}

	// 4. Global wildcard *:*
	adminScopes := []string{"*:*"}
	if !auth.VerifyScope(adminScopes, "write", "project", "openSUSE:Factory") {
		t.Errorf("expected ALLOW for superuser wildcard")
	}
}

func TestPATGenerationAndVerification(t *testing.T) {
	rawToken, pat, err := auth.GeneratePAT("user-123", "ci-agent-token", []string{"project:write:home:bot"})
	if err != nil {
		t.Fatalf("failed to generate PAT: %v", err)
	}

	if !strings.HasPrefix(rawToken, "bsgo_pat_") {
		t.Errorf("expected raw token to start with 'bsgo_pat_', got: %s", rawToken)
	}
	if pat.TokenHash == "" {
		t.Errorf("expected non-empty token hash")
	}

	// Verify raw token matches hash
	if !auth.ValidatePAT(rawToken, pat.TokenHash) {
		t.Errorf("token validation failed for correct raw token")
	}

	// Tampered token fails
	if auth.ValidatePAT(rawToken+"tampered", pat.TokenHash) {
		t.Errorf("tampered token should fail validation")
	}
}

func TestJWTValidationAndJITProvisioning(t *testing.T) {
	ctx := context.Background()
	signingKey := []byte("secret-test-key-for-authentik")
	validator := auth.NewJWTValidator(signingKey)
	userStore := auth.NewMemoryUserStore()
	provisioner := auth.NewJITProvisioner(userStore)

	// Create signed test JWT
	claims := auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "authentik-sub-alice-01",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
		PreferredUsername: "alice",
		Email:             "alice@opensuse.org",
		Groups:            []string{"developers"},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(signingKey)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	// Validate JWT
	parsedClaims, err := validator.Validate(tokenStr)
	if err != nil {
		t.Fatalf("JWT validation failed: %v", err)
	}
	if parsedClaims.PreferredUsername != "alice" {
		t.Errorf("expected username alice, got %s", parsedClaims.PreferredUsername)
	}

	// JIT Provision user into database
	user, err := provisioner.EnsureUser(ctx, parsedClaims)
	if err != nil {
		t.Fatalf("failed to JIT provision user: %v", err)
	}
	if user.Username != "alice" || user.Email != "alice@opensuse.org" {
		t.Errorf("unexpected user record: %+v", user)
	}

	// Repeat request with updated email -> should update existing user record
	parsedClaims.Email = "alice.new@opensuse.org"
	updatedUser, err := provisioner.EnsureUser(ctx, parsedClaims)
	if err != nil {
		t.Fatalf("failed to update user on subsequent login: %v", err)
	}
	if updatedUser.Email != "alice.new@opensuse.org" {
		t.Errorf("expected email to be updated to alice.new@opensuse.org, got %s", updatedUser.Email)
	}
}

func TestAuthorizerProjectACLs(t *testing.T) {
	authorizer := auth.NewAuthorizer()

	// 1. Assign project maintainer
	authorizer.SetProjectMaintainer("home:alice", "alice", "")
	authorizer.SetProjectMaintainer("openSUSE:Factory", "", "factory-team")

	userAlice := &auth.User{
		Username: "alice",
		Groups:   []string{"developers"},
	}
	userBob := &auth.User{
		Username: "bob",
		Groups:   []string{"developers"},
	}
	superAdmin := &auth.User{
		Username:    "admin",
		IsSuperuser: true,
	}

	// Alice can write to home:alice
	if !authorizer.CanWrite(userAlice, "home:alice") {
		t.Errorf("expected Alice to have write access to home:alice")
	}

	// Bob CANNOT write to home:alice
	if authorizer.CanWrite(userBob, "home:alice") {
		t.Errorf("expected Bob to be denied write access to home:alice")
	}

	// Superuser can write to anything
	if !authorizer.CanWrite(superAdmin, "openSUSE:Factory") {
		t.Errorf("expected Superuser to have write access to openSUSE:Factory")
	}
}
