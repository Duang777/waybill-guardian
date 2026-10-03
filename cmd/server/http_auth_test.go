package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/golang-jwt/jwt/v5"
)

type testJWTClaims struct {
	jwt.RegisteredClaims
	TenantID     string   `json:"tenant_id"`
	Roles        []string `json:"roles"`
	WaybillAll   bool     `json:"waybill_all"`
	WaybillIDs   []string `json:"waybill_ids"`
	EventSources []string `json:"event_sources,omitempty"`
	EventTypes   []string `json:"event_types,omitempty"`
}

func TestJWTHandlerRequiresAuthenticationOnEveryAPIRoute(t *testing.T) {
	access, privateKey := newJWTAccess(t)
	_ = privateKey
	handler := newHandler(nil, access)

	tests := []struct {
		method string
		path   string
		body   io.Reader
	}{
		{method: http.MethodPost, path: "/api/demo/trigger"},
		{method: http.MethodGet, path: "/api/runs?status=active"},
		{method: http.MethodGet, path: "/api/runs/run-1"},
		{method: http.MethodGet, path: "/api/runs/run-1/timeline"},
		{method: http.MethodGet, path: "/api/approvals?status=pending"},
		{method: http.MethodPost, path: "/api/approvals/approval-1/confirm"},
		{
			method: http.MethodPost,
			path:   "/api/approvals/approval-1/reject",
			body:   strings.NewReader(`{"reason":"no"}`),
		},
		{method: http.MethodGet, path: "/api/waybills/YD2026101001"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://api.example"+test.path, test.body)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			assertProblem(t, response.Result(), http.StatusUnauthorized, "unauthenticated")
			if response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q", response.Header().Get("WWW-Authenticate"))
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "http://api.example/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d", response.Code)
	}
}

func TestAuthenticateAPILimitsRequestToCredentialLifetime(t *testing.T) {
	verifierNow := time.Unix(1, 0).UTC()
	access, privateKey := newJWTAccessWithClock(
		t,
		func() time.Time { return verifierNow },
	)
	claims := testJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://issuer.example",
			Subject:   "reviewer-42",
			Audience:  jwt.ClaimStrings{"waybill-guardian"},
			IssuedAt:  jwt.NewNumericDate(time.Unix(0, 0).UTC()),
			NotBefore: jwt.NewNumericDate(time.Unix(0, 0).UTC()),
			ExpiresAt: jwt.NewNumericDate(time.Unix(2, 0).UTC()),
		},
		TenantID:   "tenant-a",
		Roles:      []string{"viewer"},
		WaybillAll: true,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticateAPI(access, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			t.Fatalf("request context error = %v", r.Context().Err())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://api.example/api/runs", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestJWTHandlerEnforcesTenantRoleAndWaybillScope(t *testing.T) {
	service, mock := newHTTPAuthService(t)
	access, privateKey := newJWTAccess(t)
	server := httptest.NewServer(newHandler(service, access))
	defer server.Close()

	run, err := service.StartDemo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForHTTPApproval(t, service, run.RunID)

	wrongTenant := authToken(t, privateKey, "tenant-b", []string{"viewer"}, true, nil)
	response := doAuthenticatedRequest(
		t,
		http.MethodGet,
		server.URL+"/api/waybills/"+string(run.WaybillID),
		wrongTenant,
		nil,
	)
	assertProblem(t, response, http.StatusForbidden, "forbidden")

	wrongWaybill := authToken(
		t,
		privateKey,
		"tenant-a",
		[]string{"viewer", "operator"},
		false,
		[]string{"YD2026101002"},
	)
	for _, path := range []string{
		"/api/waybills/" + string(run.WaybillID),
		"/api/runs/" + string(run.RunID),
		"/api/runs/" + string(run.RunID) + "/timeline",
	} {
		response = doAuthenticatedRequest(t, http.MethodGet, server.URL+path, wrongWaybill, nil)
		assertProblem(t, response, http.StatusForbidden, "forbidden")
	}
	response = doAuthenticatedRequest(
		t,
		http.MethodPost,
		server.URL+"/api/approvals/"+string(pending.ID)+"/confirm",
		wrongWaybill,
		nil,
	)
	assertProblem(t, response, http.StatusForbidden, "forbidden")
	if mock.WriteCount(domain.ActionReassign) != 0 {
		t.Fatal("unauthorized approval executed a platform write")
	}

	for _, path := range []string{
		"/api/runs?status=active",
		"/api/approvals?status=pending",
	} {
		response = doAuthenticatedRequest(t, http.MethodGet, server.URL+path, wrongWaybill, nil)
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			t.Fatalf("GET %s status = %d body=%s", path, response.StatusCode, body)
		}
		var payload map[string][]json.RawMessage
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		for key, values := range payload {
			if len(values) != 0 {
				t.Fatalf("%s leaked %d unauthorized values", key, len(values))
			}
		}
	}

	readOnly := authToken(
		t,
		privateKey,
		"tenant-a",
		[]string{"viewer"},
		false,
		[]string{string(run.WaybillID)},
	)
	response = doAuthenticatedRequest(
		t,
		http.MethodPost,
		server.URL+"/api/approvals/"+string(pending.ID)+"/confirm",
		readOnly,
		nil,
	)
	assertProblem(t, response, http.StatusForbidden, "forbidden")
	current, err := service.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != approval.StatusPending {
		t.Fatalf("approval status = %q, want pending", current.Status)
	}
}

func TestJWTHandlerUsesVerifiedSubjectForApproval(t *testing.T) {
	service, _ := newHTTPAuthService(t)
	access, privateKey := newJWTAccess(t)
	server := httptest.NewServer(newHandler(service, access))
	defer server.Close()

	run, err := service.StartDemo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForHTTPApproval(t, service, run.RunID)
	token := authToken(
		t,
		privateKey,
		"tenant-a",
		[]string{"operator"},
		false,
		[]string{string(run.WaybillID)},
	)
	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/api/approvals/"+string(pending.ID)+"/confirm",
		http.NoBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Actor", "forged-reviewer")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("confirm status = %d body=%s", response.StatusCode, body)
	}
	var decided approval.Approval
	if err := json.NewDecoder(response.Body).Decode(&decided); err != nil {
		t.Fatal(err)
	}
	if decided.DecidedBy != "reviewer-42" {
		t.Fatalf("decided_by = %q, want reviewer-42", decided.DecidedBy)
	}
}

func TestJWTHandlerRequiresRunCreationRole(t *testing.T) {
	service, _ := newHTTPAuthService(t)
	access, privateKey := newJWTAccess(t)
	server := httptest.NewServer(newHandler(service, access))
	defer server.Close()

	viewer := authToken(t, privateKey, "tenant-a", []string{"viewer"}, true, nil)
	response := doAuthenticatedRequest(
		t,
		http.MethodPost,
		server.URL+"/api/demo/trigger",
		viewer,
		nil,
	)
	assertProblem(t, response, http.StatusForbidden, "forbidden")

	dispatcher := authToken(t, privateKey, "tenant-a", []string{"dispatcher"}, true, nil)
	response = doAuthenticatedRequest(
		t,
		http.MethodPost,
		server.URL+"/api/demo/trigger",
		dispatcher,
		nil,
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("trigger status = %d body=%s", response.StatusCode, body)
	}
}

func TestHTTPAccessFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_ISSUER", "")
	t.Setenv("AUTH_JWT_AUDIENCE", "")
	t.Setenv("AUTH_JWT_PUBLIC_KEY_FILE", "")

	local, err := httpAccessFromEnv(httpauth.ModeLocal, "local-demo")
	if err != nil {
		t.Fatal(err)
	}
	if local.Mode() != httpauth.ModeLocal {
		t.Fatalf("local mode = %q", local.Mode())
	}
	if _, err := httpAccessFromEnv(httpauth.ModeJWT, "tenant-a"); err == nil {
		t.Fatal("JWT authentication accepted a missing public key file")
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "jwt-public.pem")
	if err := os.WriteFile(keyPath, rsaPublicKeyPEM(t, &privateKey.PublicKey), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTH_JWT_ISSUER", "https://issuer.example")
	t.Setenv("AUTH_JWT_AUDIENCE", "waybill-guardian")
	t.Setenv("AUTH_JWT_PUBLIC_KEY_FILE", keyPath)
	configured, err := httpAccessFromEnv(httpauth.ModeJWT, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if configured.Mode() != httpauth.ModeJWT {
		t.Fatalf("JWT mode = %q", configured.Mode())
	}
}

func TestTenantIDFromEnv(t *testing.T) {
	t.Setenv("TENANT_ID", "")
	tenantID, err := tenantIDFromEnv(httpauth.ModeLocal)
	if err != nil || tenantID != "local-demo" {
		t.Fatalf("local tenant = %q, %v", tenantID, err)
	}
	if _, err := tenantIDFromEnv(httpauth.ModeJWT); err == nil {
		t.Fatal("JWT authentication accepted an implicit tenant")
	}
	t.Setenv("TENANT_ID", " tenant-a ")
	tenantID, err = tenantIDFromEnv(httpauth.ModeJWT)
	if err != nil || tenantID != "tenant-a" {
		t.Fatalf("JWT tenant = %q, %v", tenantID, err)
	}
}

func newHTTPAuthService(t *testing.T) (*guardian.Service, *platform.Mock) {
	t.Helper()
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:   t.TempDir(),
		Clients:   clients,
		StepDelay: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	return service, mock
}

func newJWTAccess(t *testing.T) (*httpauth.Boundary, *rsa.PrivateKey) {
	return newJWTAccessWithClock(t, nil)
}

func newJWTAccessWithClock(
	t *testing.T,
	clock func() time.Time,
) (*httpauth.Boundary, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	access, err := httpauth.New(httpauth.Config{
		Mode:     httpauth.ModeJWT,
		TenantID: "tenant-a",
		JWT: &httpauth.JWTConfig{
			Issuer:       "https://issuer.example",
			Audience:     "waybill-guardian",
			PublicKeyPEM: rsaPublicKeyPEM(t, &privateKey.PublicKey),
			Clock:        clock,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return access, privateKey
}

func rsaPublicKeyPEM(t *testing.T, publicKey *rsa.PublicKey) []byte {
	t.Helper()
	raw, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})
}

func authToken(
	t *testing.T,
	privateKey *rsa.PrivateKey,
	tenantID string,
	roles []string,
	waybillAll bool,
	waybillIDs []string,
) string {
	t.Helper()
	now := time.Now()
	claims := testJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://issuer.example",
			Subject:   "reviewer-42",
			Audience:  jwt.ClaimStrings{"waybill-guardian"},
			IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		TenantID:   tenantID,
		Roles:      roles,
		WaybillAll: waybillAll,
		WaybillIDs: waybillIDs,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func doAuthenticatedRequest(
	t *testing.T,
	method string,
	url string,
	token string,
	body io.Reader,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertProblem(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want %d; body=%s", response.StatusCode, status, body)
	}
	var problem map[string]map[string]string
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem["error"]["code"] != code {
		t.Fatalf("error code = %q, want %q", problem["error"]["code"], code)
	}
}
