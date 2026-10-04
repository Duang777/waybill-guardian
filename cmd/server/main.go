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
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/metrics"
	"github.com/Duang777/waybill-guardian/internal/outbox"
	"github.com/Duang777/waybill-guardian/internal/outboxhttp"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
	"github.com/Duang777/waybill-guardian/internal/platform/tmssandbox"
	"github.com/Duang777/waybill-guardian/internal/storage"
	postgresstore "github.com/Duang777/waybill-guardian/internal/storage/postgres"
	"github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/google/uuid"
)

const (
	defaultHTTPAddr   = "127.0.0.1:8080"
	fixtureReadSource = "fixture-v1"
)

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

type realPlatformRuntimeConfig struct {
	profileID     string
	readSource    string
	reconcilePoll time.Duration
	adapter       tmssandbox.Config
}

type platformRuntime struct {
	reads         platform.ReadSet
	writeRuntime  platform.WriteRuntime
	activeActions []domain.Action
	profileID     string
	readSource    string
	reconcilePoll time.Duration
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
	allowNonLoopbackLocal, err := boolEnv("ALLOW_NON_LOOPBACK_LOCAL", false)
	if err != nil {
		return err
	}
	if err := validateHTTPAddr(httpAddr, authMode, allowNonLoopbackLocal); err != nil {
		return err
	}
	trustedLocalRemotes, err := localTrustedRemotes(
		httpAddr,
		authMode,
		allowNonLoopbackLocal,
	)
	if err != nil {
		return err
	}
	crossOrigin, err := crossOriginProtection(os.Getenv("ALLOWED_ORIGINS"))
	if err != nil {
		return err
	}
	frontend, err := staticFileHandler(os.Getenv("WEB_STATIC_DIR"))
	if err != nil {
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
	runtimeProfile, err := openPlatformRuntime(
		context.Background(),
		platformMode,
		tenantID,
	)
	if err != nil {
		return err
	}
	historyRetention, err := strictDurationEnv("HISTORY_RETENTION", 7*24*time.Hour)
	if err != nil {
		return err
	}
	maxConcurrentRuns, err := positiveIntEnv("MAX_CONCURRENT_RUNS", 8, 64)
	if err != nil {
		return err
	}
	evidenceStepMinutes, err := positiveFloatEnv("EVIDENCE_STEP_MINUTES", 8)
	if err != nil {
		return err
	}
	modelConfig, err := modelConfigFromEnv()
	if err != nil {
		return err
	}
	commonConfig := guardian.Config{
		DataDir:             envOr("DATA_DIR", "data"),
		Reads:               runtimeProfile.reads,
		WriteRuntime:        runtimeProfile.writeRuntime,
		ActiveActions:       runtimeProfile.activeActions,
		PlatformProfile:     runtimeProfile.profileID,
		ReadSource:          runtimeProfile.readSource,
		ApprovalTTL:         durationEnv("APPROVAL_TTL", 10*time.Minute),
		HistoryRetention:    historyRetention,
		StepDelay:           durationEnv("DEMO_STEP_DELAY", 220*time.Millisecond),
		MaxConcurrentRuns:   maxConcurrentRuns,
		EvidenceStepMinutes: evidenceStepMinutes,
		Model:               modelConfig,
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
				EffectLeaseTTL: durationEnv("EFFECT_LEASE_TTL", 15*time.Second),
				OutboxLeaseTTL: eventConfig.outboxLeaseTTL,
				WriteRuntime:   runtimeProfile.writeRuntime,
			},
		)
		if err != nil {
			return err
		}
		if platformMode == "real" {
			coverageCtx, cancelCoverage := context.WithTimeout(
				context.Background(),
				databaseConfig.StartupTimeout,
			)
			coverageErr := repository.ValidateRecoveryCoverage(coverageCtx)
			cancelCoverage()
			if coverageErr != nil {
				return coverageErr
			}
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
	slog.Info(
		"platform runtime configured",
		"mode", platformMode,
		"profile", runtimeProfile.profileID,
		"read_source", runtimeProfile.readSource,
		"write_actions", runtimeProfile.writeRuntime.AdvertisedActions(),
		"effect_reconcile_poll_interval", runtimeProfile.reconcilePoll,
	)
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
		Addr: httpAddr,
		Handler: newHandlerWithFrontend(
			service,
			access,
			repository,
			recorder,
			frontend,
			trustedLocalRemotes,
			crossOrigin,
		),
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
	if runtimeProfile.reconcilePoll > 0 {
		components = append(components, func(ctx context.Context) error {
			return service.RunEffectReconciler(ctx, runtimeProfile.reconcilePoll)
		})
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
	case "file":
		if storageMode != storage.ModeJSONL {
			return fmt.Errorf("PLATFORM=file requires STORAGE=jsonl")
		}
		if authMode != httpauth.ModeLocal {
			return fmt.Errorf("PLATFORM=file requires AUTH_MODE=local")
		}
	case "real":
		if storageMode != storage.ModePostgres {
			return fmt.Errorf("PLATFORM=real requires STORAGE=postgres")
		}
		if authMode != httpauth.ModeJWT {
			return fmt.Errorf("PLATFORM=real requires AUTH_MODE=jwt")
		}
	default:
		return fmt.Errorf("PLATFORM must be mock, file, or real")
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

func positiveFloatEnv(name string, fallback float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", name)
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

func modelConfigFromEnv() (agentkit.ModelConfig, error) {
	mode := envOr("AGENT_MODE", agentkit.ModeOffline)
	config := agentkit.ModelConfig{
		Mode:     mode,
		APIStyle: envOr("LLM_API_STYLE", agentkit.APIStyleResponses),
		BaseURL:  strings.TrimSpace(os.Getenv("LLM_BASE_URL")),
		APIKey:   strings.TrimSpace(os.Getenv("LLM_API_KEY")),
		Model:    strings.TrimSpace(os.Getenv("LLM_MODEL")),
	}
	if !strings.EqualFold(strings.TrimSpace(mode), agentkit.ModeOnline) {
		return config, nil
	}
	requestTimeout, err := strictDurationEnv(
		"LLM_REQUEST_TIMEOUT",
		agentkit.DefaultLLMRequestTimeout,
	)
	if err != nil {
		return agentkit.ModelConfig{}, err
	}
	maxOutputTokens, err := positiveIntEnv(
		"LLM_MAX_OUTPUT_TOKENS",
		agentkit.DefaultLLMMaxOutputTokens,
		agentkit.MaxLLMMaxOutputTokens,
	)
	if err != nil {
		return agentkit.ModelConfig{}, err
	}
	config.RequestTimeout = requestTimeout
	config.MaxOutputTokens = maxOutputTokens
	return config, nil
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

func validateHTTPAddr(
	addr string,
	authMode httpauth.Mode,
	allowNonLoopbackLocal bool,
) error {
	parsed, err := netip.ParseAddrPort(addr)
	if err != nil {
		return fmt.Errorf("invalid HTTP_ADDR %q: %w", addr, err)
	}
	if authMode == httpauth.ModeLocal &&
		!parsed.Addr().IsLoopback() &&
		!allowNonLoopbackLocal {
		return fmt.Errorf(
			"HTTP_ADDR must use a loopback IP address unless ALLOW_NON_LOOPBACK_LOCAL=true",
		)
	}
	return nil
}

func localTrustedRemotes(
	httpAddr string,
	authMode httpauth.Mode,
	allowNonLoopbackLocal bool,
) ([]netip.Addr, error) {
	if authMode != httpauth.ModeLocal || !allowNonLoopbackLocal {
		return nil, nil
	}
	listener, err := netip.ParseAddrPort(httpAddr)
	if err != nil || listener.Addr().IsLoopback() {
		return nil, nil
	}
	value := strings.TrimSpace(os.Getenv("LOCAL_TRUSTED_REMOTE"))
	if value == "" {
		return nil, fmt.Errorf(
			"LOCAL_TRUSTED_REMOTE is required when local mode listens on a non-loopback IP",
		)
	}
	if value == "container-gateway" {
		address, gatewayErr := defaultIPv4Gateway()
		if gatewayErr != nil {
			return nil, gatewayErr
		}
		return []netip.Addr{address}, nil
	}
	if address, parseErr := netip.ParseAddr(value); parseErr == nil {
		if address.IsUnspecified() {
			return nil, fmt.Errorf("LOCAL_TRUSTED_REMOTE must resolve to a specific IP address")
		}
		return []netip.Addr{address.Unmap()}, nil
	}
	resolved, err := net.LookupIP(value)
	if err != nil {
		return nil, fmt.Errorf("resolve LOCAL_TRUSTED_REMOTE %q: %w", value, err)
	}
	addresses := make([]netip.Addr, 0, len(resolved))
	for _, address := range resolved {
		if parsed, ok := netip.AddrFromSlice(address); ok && !parsed.IsUnspecified() {
			addresses = append(addresses, parsed.Unmap())
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf(
			"LOCAL_TRUSTED_REMOTE %q did not resolve to a specific IP address",
			value,
		)
	}
	return addresses, nil
}

func defaultIPv4Gateway() (netip.Addr, error) {
	table, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read container default gateway: %w", err)
	}
	return parseDefaultIPv4Gateway(string(table))
}

func parseDefaultIPv4Gateway(table string) (netip.Addr, error) {
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != "00000000" {
			continue
		}
		flags, flagErr := strconv.ParseUint(fields[3], 16, 32)
		if flagErr != nil || flags&0x2 == 0 {
			continue
		}
		gateway, gatewayErr := strconv.ParseUint(fields[2], 16, 32)
		if gatewayErr != nil || gateway == 0 {
			continue
		}
		return netip.AddrFrom4([4]byte{
			byte(gateway),
			byte(gateway >> 8),
			byte(gateway >> 16),
			byte(gateway >> 24),
		}), nil
	}
	return netip.Addr{}, fmt.Errorf("container default IPv4 gateway was not found")
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

func openPlatformRuntime(
	ctx context.Context,
	platformMode string,
	tenantID httpauth.TenantID,
) (platformRuntime, error) {
	switch strings.ToLower(platformMode) {
	case "mock":
		reads, writeRuntime, err := tools.NewDemoRuntime()
		if err != nil {
			return platformRuntime{}, err
		}
		return platformRuntime{
			reads:        reads,
			writeRuntime: writeRuntime,
			profileID:    tools.FixtureRuntimeAdapterID,
			readSource:   fixtureReadSource,
		}, nil
	case "file":
		dataFile := strings.TrimSpace(os.Getenv("DATA_FILE"))
		if dataFile == "" {
			return platformRuntime{}, fmt.Errorf("DATA_FILE is required for PLATFORM=file")
		}
		loaded, err := filestore.Load(dataFile)
		if err != nil {
			return platformRuntime{}, err
		}
		sourceIdentity := loaded.Source.String()
		writeRuntime, err := tools.NewFixtureWriteRuntimeForSource(
			loaded.Reads,
			sourceIdentity,
		)
		if err != nil {
			return platformRuntime{}, err
		}
		return platformRuntime{
			reads:        loaded.Reads,
			writeRuntime: writeRuntime,
			profileID:    tools.FixtureRuntimeAdapterID,
			readSource:   sourceIdentity,
		}, nil
	case "real":
		reads, _, err := tools.NewDemoRuntime()
		if err != nil {
			return platformRuntime{}, err
		}
		config, err := realPlatformConfigFromEnv(tenantID)
		if err != nil {
			return platformRuntime{}, err
		}
		writeRuntime, err := tmssandbox.New(ctx, config.adapter)
		if err != nil {
			return platformRuntime{}, err
		}
		return platformRuntime{
			reads:        reads,
			writeRuntime: writeRuntime,
			activeActions: []domain.Action{
				domain.ActionGetWaybill,
				domain.ActionGetTracking,
				domain.ActionGetDriver,
				domain.ActionGetRoadWeather,
				domain.ActionReassign,
			},
			profileID:     config.profileID,
			readSource:    config.readSource,
			reconcilePoll: config.reconcilePoll,
		}, nil
	default:
		return platformRuntime{}, errors.New("PLATFORM must be mock, file, or real")
	}
}

func realPlatformConfigFromEnv(
	tenantID httpauth.TenantID,
) (realPlatformRuntimeConfig, error) {
	profileID := strings.TrimSpace(os.Getenv("REAL_PLATFORM_PROFILE"))
	if profileID != tmssandbox.ProfileID {
		return realPlatformRuntimeConfig{}, fmt.Errorf(
			"REAL_PLATFORM_PROFILE must be %s",
			tmssandbox.ProfileID,
		)
	}
	readSource := strings.TrimSpace(os.Getenv("REAL_READ_SOURCE"))
	if readSource != fixtureReadSource {
		return realPlatformRuntimeConfig{}, fmt.Errorf(
			"REAL_READ_SOURCE must be %s",
			fixtureReadSource,
		)
	}
	baseURL := strings.TrimSpace(os.Getenv("TMS_SANDBOX_BASE_URL"))
	token := strings.TrimSpace(os.Getenv("TMS_SANDBOX_TOKEN"))
	account := strings.TrimSpace(os.Getenv("TMS_SANDBOX_ACCOUNT"))
	if baseURL == "" || token == "" || account == "" {
		return realPlatformRuntimeConfig{}, fmt.Errorf(
			"TMS_SANDBOX_BASE_URL, TMS_SANDBOX_TOKEN, and TMS_SANDBOX_ACCOUNT are required",
		)
	}
	if account != string(tenantID) {
		return realPlatformRuntimeConfig{}, fmt.Errorf(
			"TMS_SANDBOX_ACCOUNT must match TENANT_ID",
		)
	}
	requestTimeout, err := strictDurationEnv("PLATFORM_REQUEST_TIMEOUT", 3*time.Second)
	if err != nil {
		return realPlatformRuntimeConfig{}, err
	}
	startupTimeout, err := strictDurationEnv("PLATFORM_STARTUP_TIMEOUT", 5*time.Second)
	if err != nil {
		return realPlatformRuntimeConfig{}, err
	}
	reconciliationHorizon, err := strictDurationEnv(
		"EFFECT_RECONCILE_HORIZON",
		24*time.Hour,
	)
	if err != nil {
		return realPlatformRuntimeConfig{}, err
	}
	reconcilePoll, err := strictDurationEnv(
		"EFFECT_RECONCILE_POLL_INTERVAL",
		time.Second,
	)
	if err != nil {
		return realPlatformRuntimeConfig{}, err
	}
	maxConsistencyWindow, err := strictDurationEnv(
		"PLATFORM_MAX_LOOKUP_CONSISTENCY_WINDOW",
		30*time.Second,
	)
	if err != nil {
		return realPlatformRuntimeConfig{}, err
	}
	return realPlatformRuntimeConfig{
		profileID:     profileID,
		readSource:    readSource,
		reconcilePoll: reconcilePoll,
		adapter: tmssandbox.Config{
			BaseURL:               baseURL,
			Token:                 token,
			Account:               account,
			RequestTimeout:        requestTimeout,
			StartupTimeout:        startupTimeout,
			ReconciliationHorizon: reconciliationHorizon,
			MaxConsistencyWindow:  maxConsistencyWindow,
			Clock:                 time.Now,
		},
	}, nil
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
