// depsmate is a GitHub App that auto-merges safe dependabot PRs:
// all github-actions bumps, and library bumps that are semver-minor/patch.
package main

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/joho/godotenv"

	"github.com/knight42/depsmate/pkg/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// .env is optional; real environment variables take precedence
	// (godotenv.Load never overwrites variables that are already set).
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.Error("loading .env failed", "err", err)
		os.Exit(1)
	}

	appID, err := strconv.ParseInt(mustEnv(logger, "DEPSMATE_APP_ID"), 10, 64)
	if err != nil {
		logger.Error("DEPSMATE_APP_ID is not an integer", "err", err)
		os.Exit(1)
	}
	webhookSecret := mustEnv(logger, "DEPSMATE_WEBHOOK_SECRET")
	keyPath := mustEnv(logger, "DEPSMATE_PRIVATE_KEY_PATH")

	atr, err := ghinstallation.NewAppsTransportKeyFromFile(http.DefaultTransport, appID, keyPath)
	if err != nil {
		logger.Error("loading app private key failed", "err", err)
		os.Exit(1)
	}

	srv := server.New(server.Config{
		WebhookSecret: []byte(webhookSecret),
		AppsTransport: atr,
		DryRun:        os.Getenv("DEPSMATE_DRY_RUN") == "true",
		Logger:        logger,
	})

	addr := envOr("DEPSMATE_LISTEN_ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("listening", "addr", addr, "dry_run", os.Getenv("DEPSMATE_DRY_RUN") == "true")
	if err := httpSrv.ListenAndServe(); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func mustEnv(logger *slog.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		logger.Error("required environment variable is not set", "key", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
