package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

const defaultHTTPAddr = "127.0.0.1:8080"

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := envOr("HTTP_ADDR", defaultHTTPAddr)
	if err := validateHTTPAddr(httpAddr); err != nil {
		return err
	}
	clients, err := platformClients()
	if err != nil {
		return err
	}
	service, err := guardian.Open(guardian.Config{
		DataDir:     envOr("DATA_DIR", "data"),
		Clients:     clients,
		ApprovalTTL: durationEnv("APPROVAL_TTL", 10*time.Minute),
		StepDelay:   durationEnv("DEMO_STEP_DELAY", 220*time.Millisecond),
		Model: agentkit.ModelConfig{
			Mode:     envOr("AGENT_MODE", agentkit.ModeDemo),
			APIStyle: envOr("LLM_API_STYLE", agentkit.APIStyleResponses),
			BaseURL:  strings.TrimSpace(os.Getenv("LLM_BASE_URL")),
			APIKey:   strings.TrimSpace(os.Getenv("LLM_API_KEY")),
			Model:    strings.TrimSpace(os.Getenv("LLM_MODEL")),
		},
	})
	if err != nil {
		return err
	}
	defer service.Close()
	if err := service.Recover(context.Background()); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              httpAddr,
		Handler:           newHandler(service),
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", httpAddr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("waybill guardian listening", "addr", listener.Addr())
	return serve(ctx, server, listener)
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

func validateHTTPAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid HTTP_ADDR %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("HTTP_ADDR must use a loopback IP address")
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
