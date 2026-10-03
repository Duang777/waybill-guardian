package httpauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
)

var (
	ErrUnauthenticated = errors.New("valid authentication is required")
	ErrForbidden       = errors.New("access is forbidden")
)

type Mode string

const (
	ModeLocal Mode = "local"
	ModeJWT   Mode = "jwt"
)

type TenantID string
type Subject string
type Role string
type Capability string

const (
	RoleViewer     Role = "viewer"
	RoleDispatcher Role = "dispatcher"
	RoleOperator   Role = "operator"

	Read           Capability = "read"
	StartRun       Capability = "run:create"
	DecideApproval Capability = "approval:decide"
)

const localSubject Subject = "local-demo-reviewer"

type Config struct {
	Mode     Mode
	TenantID TenantID
	JWT      *JWTConfig
}

type JWTConfig struct {
	Issuer       string
	Audience     string
	PublicKeyPEM []byte
	Clock        func() time.Time
	Leeway       time.Duration
}

type Principal struct {
	subject  Subject
	tenantID TenantID
	roles    map[Role]struct{}
	scope    waybillScope
}

func (p Principal) Subject() string {
	return string(p.subject)
}

type Boundary struct {
	mode     Mode
	tenantID TenantID
	verifier tokenVerifier
	local    Principal
}

type Grant struct {
	scope waybillScope
}

type tokenVerifier interface {
	Verify(string) (Principal, error)
}

type waybillScope struct {
	all bool
	ids map[domain.WaybillID]struct{}
}

type principalContextKey struct{}

func ParseMode(value string) (Mode, error) {
	switch mode := Mode(strings.ToLower(strings.TrimSpace(value))); mode {
	case "":
		return ModeLocal, nil
	case ModeLocal, ModeJWT:
		return mode, nil
	default:
		return "", fmt.Errorf("AUTH_MODE must be local or jwt")
	}
}

func New(config Config) (*Boundary, error) {
	if config.TenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}
	switch config.Mode {
	case ModeLocal:
		return &Boundary{
			mode:     config.Mode,
			tenantID: config.TenantID,
			local: Principal{
				subject:  localSubject,
				tenantID: config.TenantID,
				roles: map[Role]struct{}{
					RoleViewer:     {},
					RoleDispatcher: {},
					RoleOperator:   {},
				},
				scope: waybillScope{all: true},
			},
		}, nil
	case ModeJWT:
		if config.JWT == nil {
			return nil, fmt.Errorf("JWT configuration is required")
		}
		verifier, err := newJWTVerifier(*config.JWT)
		if err != nil {
			return nil, err
		}
		return &Boundary{
			mode:     config.Mode,
			tenantID: config.TenantID,
			verifier: verifier,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", config.Mode)
	}
}

func (b *Boundary) Mode() Mode {
	return b.mode
}

func (b *Boundary) Authenticate(request *http.Request) (*http.Request, error) {
	var principal Principal
	switch b.mode {
	case ModeLocal:
		principal = b.local
	case ModeJWT:
		token, err := bearerToken(request.Header.Values("Authorization"))
		if err != nil {
			return nil, err
		}
		principal, err = b.verifier.Verify(token)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrUnauthenticated
	}
	ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
	return request.WithContext(ctx), nil
}

func (b *Boundary) Grant(principal Principal, capability Capability) (Grant, error) {
	if principal.subject == "" || principal.tenantID != b.tenantID {
		return Grant{}, ErrForbidden
	}
	required, ok := roleFor(capability)
	if !ok {
		return Grant{}, ErrForbidden
	}
	if _, ok := principal.roles[required]; !ok {
		return Grant{}, ErrForbidden
	}
	return Grant{scope: principal.scope}, nil
}

func PrincipalFrom(ctx context.Context) (Principal, error) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok || principal.subject == "" {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

func (g Grant) Require(id domain.WaybillID) error {
	if !g.Allows(id) {
		return ErrForbidden
	}
	return nil
}

func (g Grant) Allows(id domain.WaybillID) bool {
	if g.scope.all {
		return true
	}
	_, ok := g.scope.ids[id]
	return ok
}

func roleFor(capability Capability) (Role, bool) {
	switch capability {
	case Read:
		return RoleViewer, true
	case StartRun:
		return RoleDispatcher, true
	case DecideApproval:
		return RoleOperator, true
	default:
		return "", false
	}
}

func bearerToken(values []string) (string, error) {
	if len(values) != 1 {
		return "", ErrUnauthenticated
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
		return "", ErrUnauthenticated
	}
	return fields[1], nil
}
