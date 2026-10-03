package httpauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		value string
		want  Mode
		ok    bool
	}{
		{value: "", want: ModeLocal, ok: true},
		{value: " LOCAL ", want: ModeLocal, ok: true},
		{value: "jwt", want: ModeJWT, ok: true},
		{value: "trusted-header", ok: false},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := ParseMode(test.value)
			if test.ok && (err != nil || got != test.want) {
				t.Fatalf("ParseMode(%q) = %q, %v", test.value, got, err)
			}
			if !test.ok && err == nil {
				t.Fatalf("ParseMode(%q) unexpectedly succeeded", test.value)
			}
		})
	}
}

func TestLocalBoundaryUsesTrustedIdentity(t *testing.T) {
	boundary, err := New(Config{Mode: ModeLocal, TenantID: "local-demo"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/runs", nil)
	request.Header.Set("Authorization", "Bearer attacker")
	request.Header.Set("X-Actor", "forged-reviewer")

	authenticated, err := boundary.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := PrincipalFrom(authenticated.Context())
	if err != nil {
		t.Fatal(err)
	}
	if principal.Subject() != "local-demo-reviewer" {
		t.Fatalf("subject = %q", principal.Subject())
	}
	for _, capability := range []Capability{Read, StartRun, DecideApproval} {
		grant, grantErr := boundary.Grant(principal, capability)
		if grantErr != nil {
			t.Fatalf("grant %q: %v", capability, grantErr)
		}
		if grantErr := grant.Require("YD9999999999"); grantErr != nil {
			t.Fatalf("local grant rejected a waybill: %v", grantErr)
		}
	}
}

func TestJWTBoundaryAuthenticatesAndAuthorizes(t *testing.T) {
	now := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	privateKey := newRSAKey(t)
	boundary := newTestJWTBoundary(t, now, &privateKey.PublicKey)
	token := signToken(t, privateKey, validClaims(now))
	request := bearerRequest(token)

	authenticated, err := boundary.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := PrincipalFrom(authenticated.Context())
	if err != nil {
		t.Fatal(err)
	}
	if principal.Subject() != "reviewer-42" {
		t.Fatalf("subject = %q", principal.Subject())
	}
	grant, err := boundary.Grant(principal, DecideApproval)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Require("YD2026101001"); err != nil {
		t.Fatal(err)
	}
	if err := grant.Require("YD2026101002"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other waybill error = %v", err)
	}
	if _, err := boundary.Grant(principal, StartRun); !errors.Is(err, ErrForbidden) {
		t.Fatalf("dispatcher grant error = %v", err)
	}
}

func TestJWTBoundaryRejectsInvalidCredentials(t *testing.T) {
	now := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	privateKey := newRSAKey(t)
	otherKey := newRSAKey(t)
	boundary := newTestJWTBoundary(t, now, &privateKey.PublicKey)

	tests := []struct {
		name   string
		header []string
		token  func() string
	}{
		{name: "missing header"},
		{name: "empty bearer", header: []string{"Bearer"}},
		{name: "wrong scheme", header: []string{"Basic value"}},
		{name: "multiple headers", header: []string{"Bearer one", "Bearer two"}},
		{name: "bad signature", token: func() string {
			return signToken(t, otherKey, validClaims(now))
		}},
		{name: "wrong algorithm", token: func() string {
			return signTokenWithMethod(t, jwt.SigningMethodHS256, []byte("secret"), validClaims(now))
		}},
		{name: "wrong issuer", token: func() string {
			claims := validClaims(now)
			claims.Issuer = "https://other.example"
			return signToken(t, privateKey, claims)
		}},
		{name: "wrong audience", token: func() string {
			claims := validClaims(now)
			claims.Audience = jwt.ClaimStrings{"other-service"}
			return signToken(t, privateKey, claims)
		}},
		{name: "expired", token: func() string {
			claims := validClaims(now)
			claims.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
			return signToken(t, privateKey, claims)
		}},
		{name: "future issued at", token: func() string {
			claims := validClaims(now)
			claims.IssuedAt = jwt.NewNumericDate(now.Add(time.Minute))
			return signToken(t, privateKey, claims)
		}},
		{name: "missing issued at", token: func() string {
			claims := validClaims(now)
			claims.IssuedAt = nil
			return signToken(t, privateKey, claims)
		}},
		{name: "missing subject", token: func() string {
			claims := validClaims(now)
			claims.Subject = ""
			return signToken(t, privateKey, claims)
		}},
		{name: "padded subject", token: func() string {
			claims := validClaims(now)
			claims.Subject = " reviewer-42 "
			return signToken(t, privateKey, claims)
		}},
		{name: "missing tenant", token: func() string {
			claims := validClaims(now)
			claims.TenantID = ""
			return signToken(t, privateKey, claims)
		}},
		{name: "padded tenant", token: func() string {
			claims := validClaims(now)
			claims.TenantID = " tenant-a "
			return signToken(t, privateKey, claims)
		}},
		{name: "unknown role", token: func() string {
			claims := validClaims(now)
			claims.Roles = []string{"administrator"}
			return signToken(t, privateKey, claims)
		}},
		{name: "duplicate role", token: func() string {
			claims := validClaims(now)
			claims.Roles = []string{"operator", "operator"}
			return signToken(t, privateKey, claims)
		}},
		{name: "empty scope", token: func() string {
			claims := validClaims(now)
			claims.WaybillIDs = nil
			return signToken(t, privateKey, claims)
		}},
		{name: "conflicting scope", token: func() string {
			claims := validClaims(now)
			claims.WaybillAll = true
			return signToken(t, privateKey, claims)
		}},
		{name: "invalid waybill", token: func() string {
			claims := validClaims(now)
			claims.WaybillIDs = []string{"other"}
			return signToken(t, privateKey, claims)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/runs", nil)
			switch {
			case test.token != nil:
				request.Header.Set("Authorization", "Bearer "+test.token())
			default:
				for _, value := range test.header {
					request.Header.Add("Authorization", value)
				}
			}
			if _, err := boundary.Authenticate(request); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Authenticate() error = %v", err)
			}
		})
	}
}

func TestJWTBoundaryRejectsWeakRSAKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		Mode:     ModeJWT,
		TenantID: "tenant-a",
		JWT: &JWTConfig{
			Issuer:       "https://issuer.example",
			Audience:     "waybill-guardian",
			PublicKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}),
		},
	}); err == nil {
		t.Fatal("1024-bit RSA key was accepted")
	}
}

func TestJWTBoundaryRejectsWrongTenantAndMissingRole(t *testing.T) {
	now := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	privateKey := newRSAKey(t)
	boundary := newTestJWTBoundary(t, now, &privateKey.PublicKey)

	wrongTenant := validClaims(now)
	wrongTenant.TenantID = "tenant-b"
	principal := authenticateClaims(t, boundary, privateKey, wrongTenant)
	if _, err := boundary.Grant(principal, Read); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong tenant grant error = %v", err)
	}

	missingRole := validClaims(now)
	missingRole.Roles = []string{"viewer"}
	principal = authenticateClaims(t, boundary, privateKey, missingRole)
	if _, err := boundary.Grant(principal, DecideApproval); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing role grant error = %v", err)
	}
}

func TestPrincipalFromRequiresAuthenticatedContext(t *testing.T) {
	if _, err := PrincipalFrom(context.Background()); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("PrincipalFrom() error = %v", err)
	}
}

func newTestJWTBoundary(
	t *testing.T,
	now time.Time,
	publicKey *rsa.PublicKey,
) *Boundary {
	t.Helper()
	raw, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := New(Config{
		Mode:     ModeJWT,
		TenantID: "tenant-a",
		JWT: &JWTConfig{
			Issuer:       "https://issuer.example",
			Audience:     "waybill-guardian",
			PublicKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}),
			Clock:        func() time.Time { return now },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return boundary
}

func validClaims(now time.Time) jwtClaims {
	return jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://issuer.example",
			Subject:   "reviewer-42",
			Audience:  jwt.ClaimStrings{"waybill-guardian"},
			IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		TenantID:   "tenant-a",
		Roles:      []string{"viewer", "operator"},
		WaybillIDs: []string{"YD2026101001"},
	}
}

func authenticateClaims(
	t *testing.T,
	boundary *Boundary,
	key *rsa.PrivateKey,
	claims jwtClaims,
) Principal {
	t.Helper()
	request := bearerRequest(signToken(t, key, claims))
	authenticated, err := boundary.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := PrincipalFrom(authenticated.Context())
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func bearerRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/runs", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims jwtClaims) string {
	t.Helper()
	return signTokenWithMethod(t, jwt.SigningMethodRS256, key, claims)
}

func signTokenWithMethod(
	t *testing.T,
	method jwt.SigningMethod,
	key any,
	claims jwtClaims,
) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
