package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/marcus/nightshift/internal/config"
	"github.com/marcus/nightshift/internal/db"
	"github.com/marcus/nightshift/internal/sidecar"
	"github.com/spf13/cobra"
)

var (
	sidecarAddr         string
	sidecarPollInterval time.Duration
	sidecarDBPath       string
)

var sidecarCmd = &cobra.Command{
	Use:   "sidecar",
	Short: "Run the quota-triggered task console",
}

var sidecarServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the local task API and quota scheduler",
	RunE:  runSidecarServe,
}

func init() {
	sidecarServeCmd.Flags().StringVar(&sidecarAddr, "addr", "127.0.0.1:8787", "Local API listen address")
	sidecarServeCmd.Flags().DurationVar(&sidecarPollInterval, "poll", 5*time.Minute, "Quota refresh interval")
	sidecarServeCmd.Flags().StringVar(&sidecarDBPath, "db", "", "Override database path")
	sidecarCmd.AddCommand(sidecarServeCmd)
	rootCmd.AddCommand(sidecarCmd)
}

func runSidecarServe(_ *cobra.Command, _ []string) error {
	ensurePATH()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	dbPath := cfg.ExpandedDBPath()
	if sidecarDBPath != "" {
		dbPath = sidecarDBPath
	}
	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = database.Close() }()
	store, err := sidecar.NewStore(database)
	if err != nil {
		return err
	}
	controller := sidecar.NewController(store, sidecar.NewLiveQuotaSource(cfg),
		sidecar.NewCLIExecutor(), sidecar.DefaultHarvestPolicy())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go runSidecarLoop(ctx, controller, sidecarPollInterval)

	server := &http.Server{
		Addr:              sidecarAddr,
		Handler:           sidecar.NewAPI(store, controller),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("sidecar API listening at http://%s\n", sidecarAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runSidecarLoop(ctx context.Context, controller *sidecar.Controller, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	run := func() {
		_ = controller.RefreshAndRun(ctx)
	}
	go run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			go run()
		}
	}
}
