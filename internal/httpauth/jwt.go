package httpauth

import (
	"crypto/rsa"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/golang-jwt/jwt/v5"
)

type jwtClaims struct {
	jwt.RegisteredClaims
	TenantID   string   `json:"tenant_id"`
	Roles      []string `json:"roles"`
	WaybillAll bool     `json:"waybill_all"`
	WaybillIDs []string `json:"waybill_ids"`
}

type jwtVerifier struct {
	parser *jwt.Parser
	key    *rsa.PublicKey
	leeway time.Duration
}

func newJWTVerifier(config JWTConfig) (*jwtVerifier, error) {
	issuer := strings.TrimSpace(config.Issuer)
	if issuer == "" {
		return nil, fmt.Errorf("AUTH_JWT_ISSUER is required for JWT authentication")
	}
	audience := strings.TrimSpace(config.Audience)
	if audience == "" {
		return nil, fmt.Errorf("AUTH_JWT_AUDIENCE is required for JWT authentication")
	}
	if len(config.PublicKeyPEM) == 0 {
		return nil, fmt.Errorf("AUTH_JWT_PUBLIC_KEY_FILE is required for JWT authentication")
	}
	key, err := jwt.ParseRSAPublicKeyFromPEM(config.PublicKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse JWT RSA public key: %w", err)
	}
	if key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("JWT RSA public key must be at least 2048 bits")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Leeway < 0 {
		return nil, fmt.Errorf("JWT clock leeway cannot be negative")
	}
	return &jwtVerifier{
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithAudience(audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(config.Leeway),
			jwt.WithTimeFunc(config.Clock),
			jwt.WithStrictDecoding(),
		),
		key:    key,
		leeway: config.Leeway,
	}, nil
}

func (v *jwtVerifier) Verify(raw string) (Principal, error) {
	var claims jwtClaims
	token, err := v.parser.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, ErrUnauthenticated
		}
		return v.key, nil
	})
	if err != nil || !token.Valid {
		return Principal{}, ErrUnauthenticated
	}
	principal, err := principalFromClaims(claims)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	principal.validUntil = claims.ExpiresAt.Time.Add(v.leeway)
	return principal, nil
}

func principalFromClaims(claims jwtClaims) (Principal, error) {
	if claims.Subject == "" ||
		claims.Subject != strings.TrimSpace(claims.Subject) ||
		claims.TenantID == "" ||
		claims.TenantID != strings.TrimSpace(claims.TenantID) ||
		claims.IssuedAt == nil ||
		claims.ExpiresAt == nil {
		return Principal{}, ErrUnauthenticated
	}
	if !claims.ExpiresAt.Time.After(claims.IssuedAt.Time) {
		return Principal{}, ErrUnauthenticated
	}
	if claims.NotBefore != nil && !claims.ExpiresAt.Time.After(claims.NotBefore.Time) {
		return Principal{}, ErrUnauthenticated
	}
	roles, err := parseRoles(claims.Roles)
	if err != nil {
		return Principal{}, err
	}
	scope, err := parseWaybillScope(claims.WaybillAll, claims.WaybillIDs)
	if err != nil {
		return Principal{}, err
	}
	return Principal{
		subject:  Subject(claims.Subject),
		tenantID: TenantID(claims.TenantID),
		roles:    roles,
		scope:    scope,
	}, nil
}

func parseRoles(values []string) (map[Role]struct{}, error) {
	if len(values) == 0 {
		return nil, ErrUnauthenticated
	}
	roles := make(map[Role]struct{}, len(values))
	for _, value := range values {
		role := Role(value)
		switch role {
		case RoleViewer, RoleDispatcher, RoleOperator:
		default:
			return nil, ErrUnauthenticated
		}
		if _, exists := roles[role]; exists {
			return nil, ErrUnauthenticated
		}
		roles[role] = struct{}{}
	}
	return roles, nil
}

func parseWaybillScope(all bool, values []string) (waybillScope, error) {
	if all {
		if len(values) != 0 {
			return waybillScope{}, ErrUnauthenticated
		}
		return waybillScope{all: true}, nil
	}
	if len(values) == 0 {
		return waybillScope{}, ErrUnauthenticated
	}
	ids := make(map[domain.WaybillID]struct{}, len(values))
	for _, value := range values {
		id := domain.WaybillID(value)
		if err := domain.ValidateWaybillID(id); err != nil {
			return waybillScope{}, ErrUnauthenticated
		}
		if _, exists := ids[id]; exists {
			return waybillScope{}, ErrUnauthenticated
		}
		ids[id] = struct{}{}
	}
	return waybillScope{ids: ids}, nil
}
