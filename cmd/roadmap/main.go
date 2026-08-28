package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"roadmap/internal/auth"
	"roadmap/internal/config"
	"roadmap/internal/db"
	"roadmap/internal/httpapi"
	"roadmap/internal/store"
)

const (
	healthcheckURL     = "http://127.0.0.1:8080/healthz"
	healthcheckTimeout = 2 * time.Second
	healthcheckMaxBody = 64 * 1024
)

func main() {
	log.SetFlags(0)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			if len(os.Args) != 2 {
				log.Fatalf("healthcheck does not accept arguments")
			}
			if err := runHealthcheck(); err != nil {
				log.Printf("healthcheck: %v", err)
				os.Exit(1)
			}
			return
		case "migration-info":
			if len(os.Args) != 2 {
				log.Fatalf("migration-info does not accept arguments")
			}
			if err := runMigrationInfo(os.Stdout); err != nil {
				log.Printf("migration-info: %v", err)
				os.Exit(1)
			}
			return
		case "schema-preflight", "migration-preflight":
			if len(os.Args) != 3 {
				log.Fatalf("%s requires exactly one database path", os.Args[1])
			}
			if err := runSchemaPreflight(context.Background(), os.Args[2], os.Stdout); err != nil {
				log.Printf("schema preflight: %v", err)
				os.Exit(1)
			}
			return
		case "migration-apply":
			if len(os.Args) != 3 {
				log.Fatalf("migration-apply requires exactly one staged database path")
			}
			if err := runMigrationApply(context.Background(), os.Args[2], os.Stdout); err != nil {
				log.Printf("migration apply: %v", err)
				os.Exit(1)
			}
			return
		default:
			log.Fatalf("unknown command %q", os.Args[1])
		}
	}
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	ctx := context.Background()
	database, err := db.Open(ctx, cfg.DB)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()
	data := store.New(database)
	if cfg.DemoSeed {
		// Demo data must not create a passwordless human in local-auth mode;
		// doing so would make first-run setup appear complete with no usable
		// login. Disabled mode already has an explicit development actor.
		seedActorID := ""
		if cfg.AuthMode == "disabled" {
			actor, seedErr := data.EnsureDisabledActor(ctx)
			if seedErr != nil {
				log.Fatalf("demo actor: %v", seedErr)
			}
			seedActorID = actor.ID
		}
		if seedErr := data.SeedDemo(ctx, seedActorID); seedErr != nil {
			log.Fatalf("demo seed: %v", seedErr)
		}
	}
	manager := auth.NewManager(data, cfg)
	api := httpapi.New(data, manager, cfg)
	server := &http.Server{Addr: cfg.Addr, Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 * 1024}
	go func() {
		log.Printf(`{"level":"info","msg":"roadmap listening","addr":%q}`, cfg.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-signalCtx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func runMigrationInfo(output io.Writer) error {
	version, digest, err := db.EmbeddedSchema()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "latest_schema_version=%d\nmigration_digest=%s\n", version, digest)
	return err
}

func runSchemaPreflight(ctx context.Context, sourcePath string, output io.Writer) error {
	inspection, err := db.Preflight(ctx, sourcePath)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "schema_version=%d\nlatest_schema_version=%d\nmigration_digest=%s\nintegrity_check=ok\nforeign_key_check=ok\nstatus=ok\n", inspection.SchemaVersion, inspection.EmbeddedSchemaVersion, inspection.MigrationDigest); err != nil {
		return err
	}
	return nil
}

func runMigrationApply(ctx context.Context, candidatePath string, output io.Writer) error {
	inspection, err := db.MigrateCandidate(ctx, candidatePath)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "schema_version=%d\nlatest_schema_version=%d\nmigration_digest=%s\nintegrity_check=ok\nforeign_key_check=ok\nstatus=ok\n", inspection.SchemaVersion, inspection.EmbeddedSchemaVersion, inspection.MigrationDigest); err != nil {
		return err
	}
	return nil
}

func runHealthcheck() error {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("unexpected default HTTP transport")
	}
	transport = transport.Clone()
	// The endpoint is intentionally fixed to loopback. Do not allow proxy
	// environment variables to turn a local liveness check into an outbound
	// request.
	transport.Proxy = nil
	client := &http.Client{
		Transport: transport,
		Timeout:   healthcheckTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer transport.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()
	return checkHealth(ctx, client, healthcheckURL)
}

func checkHealth(ctx context.Context, client *http.Client, endpoint string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("endpoint returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, healthcheckMaxBody+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(body) > healthcheckMaxBody {
		return fmt.Errorf("response exceeds %d bytes", healthcheckMaxBody)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("invalid health JSON: %w", err)
	}
	if payload.Status != "ok" {
		return fmt.Errorf("health status is %q", payload.Status)
	}
	return nil
}
