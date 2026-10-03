package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/metrics"
	"github.com/Duang777/waybill-guardian/internal/outbox"
	"github.com/Duang777/waybill-guardian/internal/outboxhttp"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/storage"
	postgresstore "github.com/Duang777/waybill-guardian/internal/storage/postgres"
	"github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/google/uuid"
)

const defaultHTTPAddr = "127.0.0.1:8080"

type eventRuntimeConfig struct {
	outboxEnabled   bool
	outboxURL       string
	outboxToken     string
	outboxBatchSize int
	outboxWorkers   int
	outboxPoll      time.Duration
	outboxLeaseTTL  time.Duration
	outboxStatsPoll time.Duration
	outboxTimeout   time.Duration
	metricsAddr     string
}

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := envOr("HTTP_ADDR", defaultHTTPAddr)
	authMode, err := httpauth.ParseMode(os.Getenv("AUTH_MODE"))
	if err != nil {
		return err
	}
	if err := validateHTTPAddr(httpAddr, authMode); err != nil {
		return err
	}
	platformMode := strings.ToLower(envOr("PLATFORM", "mock"))
	storageMode, err := storage.ParseMode(os.Getenv("STORAGE"))
	if err != nil {
		return err
	}
	if err := validateRuntimeModes(platformMode, storageMode, authMode); err != nil {
		return err
	}
	eventConfig, err := eventConfigFromEnv(storageMode)
	if err != nil {
		return err
	}
	var outboxPublisher *outboxhttp.Publisher
	if eventConfig.outboxEnabled {
		outboxPublisher, err = outboxhttp.New(outboxhttp.Config{
			URL:     eventConfig.outboxURL,
			Token:   eventConfig.outboxToken,
			Timeout: eventConfig.outboxTimeout,
		})
		if err != nil {
			return err
		}
	}
	tenantID, err := tenantIDFromEnv(authMode)
	if err != nil {
		return err
	}
	access, err := httpAccessFromEnv(authMode, tenantID)
	if err != nil {
		return err
	}
	var database *postgresstore.DB
	var repository *postgresstore.Repository
	var databaseConfig postgresstore.Config
	if storageMode == storage.ModePostgres {
		databaseConfig, err = postgresConfigFromEnv()
		if err != nil {
			return err
		}
		database, err = postgresstore.Open(context.Background(), databaseConfig)
		if err != nil {
			return err
		}
		defer database.Close()
	}
	clients, err := platformClients()
	if err != nil {
		return err
	}
	historyRetention, err := strictDurationEnv("HISTORY_RETENTION", 7*24*time.Hour)
	if err != nil {
		return err
	}
	commonConfig := guardian.Config{
		DataDir:          envOr("DATA_DIR", "data"),
		Clients:          clients,
		ApprovalTTL:      durationEnv("APPROVAL_TTL", 10*time.Minute),
		HistoryRetention: historyRetention,
		StepDelay:        durationEnv("DEMO_STEP_DELAY", 220*time.Millisecond),
		Model: agentkit.ModelConfig{
			Mode:     envOr("AGENT_MODE", agentkit.ModeDemo),
			APIStyle: envOr("LLM_API_STYLE", agentkit.APIStyleResponses),
			BaseURL:  strings.TrimSpace(os.Getenv("LLM_BASE_URL")),
			APIKey:   strings.TrimSpace(os.Getenv("LLM_API_KEY")),
			Model:    strings.TrimSpace(os.Getenv("LLM_MODEL")),
		},
	}
	var service *guardian.Service
	if storageMode == storage.ModePostgres {
		key, keyErr := checkpointKeyFromEnv()
		if keyErr != nil {
			return keyErr
		}
		repository, err = postgresstore.NewRepository(
			database,
			postgresstore.RepositoryConfig{
				TenantID:       string(tenantID),
				WorkerID:       envOr("INSTANCE_ID", uuid.NewString()),
				LeaseTTL:       durationEnv("RUN_LEASE_TTL", 30*time.Second),
				OutboxLeaseTTL: eventConfig.outboxLeaseTTL,
				EffectLookup: func(
					ctx context.Context,
					command idempotency.Command,
				) (platform.EffectResult, error) {
					request := platform.LookupEffectRequest{
						Action:         command.Identity.Action,
						IdempotencyKey: command.Identity.Key,
					}
					switch command.Identity.Action {
					case "tms.reassign", "tms.create_claim":
						return clients.TMS.LookupEffect(ctx, request)
					case "notify.send_sms":
						return clients.Notification.LookupEffect(ctx, request)
					default:
						return platform.EffectResult{
							Disposition: platform.EffectPermanentFailed,
						}, nil
					}
				},
			},
		)
		if err != nil {
			return err
		}
		historyCtx, cancelHistory := context.WithTimeout(
			context.Background(),
			databaseConfig.StartupTimeout,
		)
		history, historyErr := postgresstore.NewConversationPersistence(
			historyCtx,
			database,
			postgresstore.HistoryConfig{
				TenantID:  string(tenantID),
				KeyID:     envOr("CHECKPOINT_KEY_ID", "local-v1"),
				Key:       key,
				Retention: historyRetention,
			},
		)
		cancelHistory()
		if historyErr != nil {
			return historyErr
		}
		service, err = guardian.OpenDurable(guardian.DurableConfig{
			Config:      commonConfig,
			Journal:     repository,
			Effects:     repository,
			History:     history,
			Coordinator: repository,
		})
	} else {
		service, err = guardian.Open(commonConfig)
	}
	if err != nil {
		return err
	}
	defer service.Close()
	if err := service.Recover(context.Background()); err != nil {
		return err
	}

	var recorder *metrics.Recorder
	var dispatcher *outbox.Dispatcher
	var statsMonitor *outbox.StatsMonitor
	if storageMode == storage.ModePostgres {
		recorder = metrics.New(time.Now)
		if eventConfig.outboxEnabled {
			dispatcher, err = outbox.NewDispatcher(outbox.DispatcherConfig{
				Store:         repository,
				Publisher:     outboxPublisher,
				Observer:      recorder,
				BatchSize:     eventConfig.outboxBatchSize,
				Concurrency:   eventConfig.outboxWorkers,
				PollInterval:  eventConfig.outboxPoll,
				LeaseTTL:      eventConfig.outboxLeaseTTL,
				StatsInterval: eventConfig.outboxStatsPoll,
			})
			if err != nil {
				return err
			}
		} else if eventConfig.metricsAddr != "" {
			statsMonitor, err = outbox.NewStatsMonitor(
				repository,
				recorder,
				eventConfig.outboxStatsPoll,
			)
			if err != nil {
				return err
			}
		}
	}

	listener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", httpAddr, err)
	}
	defer listener.Close()
	var metricsListener net.Listener
	if eventConfig.metricsAddr != "" {
		metricsListener, err = net.Listen("tcp", eventConfig.metricsAddr)
		if err != nil {
			return fmt.Errorf("listen for metrics on %s: %w", eventConfig.metricsAddr, err)
		}
		defer metricsListener.Close()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Addr:              httpAddr,
		Handler:           newHandlerWithEvents(service, access, repository, recorder),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	slog.Info("waybill guardian listening", "addr", listener.Addr())
	components := []runComponent{
		func(ctx context.Context) error {
			return serve(ctx, server, listener)
		},
	}
	if dispatcher != nil {
		components = append(components, dispatcher.Run)
	}
	if statsMonitor != nil {
		components = append(components, statsMonitor.Run)
	}
	if metricsListener != nil {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("GET /metrics", recorder.Handler())
		metricsServer := &http.Server{
			Addr:              eventConfig.metricsAddr,
			Handler:           metricsMux,
			ReadHeaderTimeout: 5 * time.Second,
			BaseContext: func(net.Listener) context.Context {
				return ctx
			},
		}
		slog.Info("waybill metrics listening", "addr", metricsListener.Addr())
		components = append(components, func(ctx context.Context) error {
			return serve(ctx, metricsServer, metricsListener)
		})
	}
	return runComponents(ctx, components...)
}

type runComponent func(context.Context) error

func runComponents(ctx context.Context, components ...runComponent) error {
	if len(components) == 0 {
		return nil
	}
	componentCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, len(components))
	for _, component := range components {
		go func() {
			results <- component(componentCtx)
		}()
	}

	var errs []error
	received := 0
	select {
	case err := <-results:
		received++
		if err != nil {
			errs = append(errs, err)
		}
	case <-ctx.Done():
	}
	cancel()
	for received < len(components) {
		if err := <-results; err != nil {
			errs = append(errs, err)
		}
		received++
	}
	return errors.Join(errs...)
}

func checkpointKeyFromEnv() ([]byte, error) {
	value := strings.TrimSpace(os.Getenv("CHECKPOINT_ENCRYPTION_KEY"))
	if value == "" {
		return nil, fmt.Errorf("CHECKPOINT_ENCRYPTION_KEY is required for PostgreSQL storage")
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("CHECKPOINT_ENCRYPTION_KEY must be base64 for exactly 32 bytes")
	}
	return key, nil
}

func validateRuntimeModes(
	platformMode string,
	storageMode storage.Mode,
	authMode httpauth.Mode,
) error {
	switch platformMode {
	case "mock":
	case "real":
		if storageMode != storage.ModePostgres {
			return fmt.Errorf("PLATFORM=real requires STORAGE=postgres")
		}
		if authMode != httpauth.ModeJWT {
			return fmt.Errorf("PLATFORM=real requires AUTH_MODE=jwt")
		}
	default:
		return fmt.Errorf("PLATFORM must be mock or real")
	}
	return nil
}

func eventConfigFromEnv(storageMode storage.Mode) (eventRuntimeConfig, error) {
	enabled, err := boolEnv("OUTBOX_ENABLED", false)
	if err != nil {
		return eventRuntimeConfig{}, err
	}
	config := eventRuntimeConfig{
		outboxEnabled: enabled,
		outboxURL:     strings.TrimSpace(os.Getenv("OUTBOX_URL")),
		outboxToken:   strings.TrimSpace(os.Getenv("OUTBOX_TOKEN")),
		metricsAddr:   strings.TrimSpace(os.Getenv("METRICS_ADDR")),
	}
	if storageMode != storage.ModePostgres {
		if config.outboxEnabled || config.metricsAddr != "" {
			return eventRuntimeConfig{}, fmt.Errorf(
				"outbox dispatcher and metrics listener require STORAGE=postgres",
			)
		}
		return config, nil
	}
	config.outboxBatchSize, err = positiveIntEnv("OUTBOX_BATCH_SIZE", 10, 100)
	if err != nil {
		return eventRuntimeConfig{}, err
	}
	config.outboxWorkers, err = positiveIntEnv("OUTBOX_CONCURRENCY", 4, 100)
	if err != nil {
		return eventRuntimeConfig{}, err
	}
	config.outboxPoll, err = strictDurationEnv("OUTBOX_POLL_INTERVAL", 250*time.Millisecond)
	if err != nil {
		return eventRuntimeConfig{}, err
	}
	config.outboxLeaseTTL, err = strictDurationEnv("OUTBOX_LEASE_TTL", 30*time.Second)
	if err != nil {
		return eventRuntimeConfig{}, err
	}
	config.outboxStatsPoll, err = strictDurationEnv("OUTBOX_STATS_INTERVAL", 15*time.Second)
	if err != nil {
		return eventRuntimeConfig{}, err
	}
	config.outboxTimeout, err = strictDurationEnv("OUTBOX_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return eventRuntimeConfig{}, err
	}
	if config.outboxEnabled && (config.outboxURL == "" || config.outboxToken == "") {
		return eventRuntimeConfig{}, fmt.Errorf(
			"OUTBOX_URL and OUTBOX_TOKEN are required when OUTBOX_ENABLED=true",
		)
	}
	return config, nil
}

func tenantIDFromEnv(authMode httpauth.Mode) (httpauth.TenantID, error) {
	value := strings.TrimSpace(os.Getenv("TENANT_ID"))
	if value == "" {
		if authMode == httpauth.ModeJWT {
			return "", fmt.Errorf("TENANT_ID is required for JWT authentication")
		}
		value = "local-demo"
	}
	return httpauth.TenantID(value), nil
}

func httpAccessFromEnv(
	mode httpauth.Mode,
	tenantID httpauth.TenantID,
) (*httpauth.Boundary, error) {
	config := httpauth.Config{
		Mode:     mode,
		TenantID: tenantID,
	}
	if mode == httpauth.ModeJWT {
		keyPath := strings.TrimSpace(os.Getenv("AUTH_JWT_PUBLIC_KEY_FILE"))
		if keyPath == "" {
			return nil, fmt.Errorf("AUTH_JWT_PUBLIC_KEY_FILE is required for JWT authentication")
		}
		publicKey, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read AUTH_JWT_PUBLIC_KEY_FILE: %w", err)
		}
		config.JWT = &httpauth.JWTConfig{
			Issuer:       strings.TrimSpace(os.Getenv("AUTH_JWT_ISSUER")),
			Audience:     strings.TrimSpace(os.Getenv("AUTH_JWT_AUDIENCE")),
			PublicKeyPEM: publicKey,
			Leeway:       30 * time.Second,
		}
	}
	return httpauth.New(config)
}

func postgresConfigFromEnv() (postgresstore.Config, error) {
	maxConns, err := int32Env("PG_MAX_CONNS", 8)
	if err != nil {
		return postgresstore.Config{}, err
	}
	minConns, err := int32Env("PG_MIN_CONNS", 0)
	if err != nil {
		return postgresstore.Config{}, err
	}
	startupTimeout, err := strictDurationEnv("PG_STARTUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return postgresstore.Config{}, err
	}
	return postgresstore.Config{
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		MaxConns:       maxConns,
		MinConns:       minConns,
		StartupTimeout: startupTimeout,
	}, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}

func positiveIntEnv(name string, fallback, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer between 1 and %d", name, maximum)
	}
	return parsed, nil
}

func int32Env(name string, fallback int32) (int32, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return int32(parsed), nil
}

func strictDurationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func serve(ctx context.Context, server *http.Server, listener net.Listener) error {
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()

	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, server.Close())
		}
		serveErr := <-serveDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func validateHTTPAddr(addr string, authMode httpauth.Mode) error {
	parsed, err := netip.ParseAddrPort(addr)
	if err != nil {
		return fmt.Errorf("invalid HTTP_ADDR %q: %w", addr, err)
	}
	if authMode == httpauth.ModeLocal && !parsed.Addr().IsLoopback() {
		return fmt.Errorf("HTTP_ADDR must use a loopback IP address")
	}
	return nil
}

func validateRequestHost(value string) error {
	if address, err := netip.ParseAddr(value); err == nil {
		if address.IsLoopback() {
			return nil
		}
		return fmt.Errorf("request host must use a loopback IP address")
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		address, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
		if err == nil && address.IsLoopback() {
			return nil
		}
		return fmt.Errorf("request host must use a loopback IP address")
	}
	address, err := netip.ParseAddrPort(value)
	if err != nil || !address.Addr().IsLoopback() {
		return fmt.Errorf("request host must use a loopback IP address")
	}
	return nil
}

func platformClients() (platform.Clients, error) {
	switch strings.ToLower(envOr("PLATFORM", "mock")) {
	case "mock":
		clients, _, err := tools.NewDemoClients()
		return clients, err
	case "real":
		return platform.Clients{}, platform.ErrNotImplemented
	default:
		return platform.Clients{}, errors.New("PLATFORM must be mock or real")
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		slog.Warn("invalid duration; using default", "name", name, "value", value, "default", fallback)
		return fallback
	}
	return parsed
}
