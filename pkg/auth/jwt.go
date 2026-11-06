package auth

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

type JWTValidator struct {
	key      any
	issuer   string
	audience string
}

func NewJWTValidator(signingKey []byte) *JWTValidator {
	return &JWTValidator{key: signingKey}
}

func NewAsymmetricJWTValidator(publicKey any, issuer, audience string) *JWTValidator {
	return &JWTValidator{
		key:      publicKey,
		issuer:   issuer,
		audience: audience,
	}
}

func (v *JWTValidator) WithIssuer(issuer string) *JWTValidator {
	v.issuer = issuer
	return v
}

func (v *JWTValidator) WithAudience(audience string) *JWTValidator {
	v.audience = audience
	return v
}

func (v *JWTValidator) Validate(tokenString string) (*Claims, error) {
	var parserOpts []jwt.ParserOption
	if v.issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(v.issuer))
	}
	if v.audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(v.audience))
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		switch v.key.(type) {
		case *rsa.PublicKey:
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method for RSA: %v", token.Header["alg"])
			}
		case *ecdsa.PublicKey:
			if _, ok := token.Method.(*jwt.SigningMethodECDSA); !ok {
				return nil, fmt.Errorf("unexpected signing method for ECDSA: %v", token.Header["alg"])
			}
		case []byte:
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method for HMAC: %v", token.Header["alg"])
			}
		default:
			return nil, fmt.Errorf("unsupported key type: %T", v.key)
		}
		return v.key, nil
	}, parserOpts...)

	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}
