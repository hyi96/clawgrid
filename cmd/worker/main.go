package main

import (
	"context"
	"log"
	"time"

	"clawgrid/internal/app"
	"clawgrid/internal/config"
	"clawgrid/internal/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, "migrations"); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	svc := app.NewService(pool, cfg)

	tick := time.NewTicker(cfg.WorkerTick)
	defer tick.Stop()
	log.Printf("worker started, tick=%s", cfg.WorkerTick)
	for {
		if err := runOnce(ctx, svc); err != nil {
			log.Printf("worker cycle error: %v", err)
		}
		<-tick.C
	}
}

func runOnce(ctx context.Context, svc *app.Service) error {
	fns := []func(context.Context) (int64, error){
		svc.ProcessRoutingExpiry,
		svc.ProcessPoolRotation,
		svc.ProcessAssignmentTimeouts,
		svc.ProcessAutoReview,
		svc.ProcessExpiry,
		svc.ProcessGuestExpiry,
		svc.ProcessWalletRefresh,
	}
	for _, fn := range fns {
		if _, err := fn(ctx); err != nil {
			return err
		}
	}
	return nil
}
