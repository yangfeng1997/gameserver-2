package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"project/src/core/app"
	"project/src/server/lobby"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if err := lobby.LoadConfigs(); err != nil {
		return fmt.Errorf("load configs: %w", err)
	}

	nodeID := os.Getenv("NODE_ID")
	if nodeID == "" {
		nodeID = "1.2.0"
	}

	app, err := app.NewBuilder(
		app.WithName("lobbysvr"),
		app.WithNodeID(nodeID),
	).AddModule(lobby.NewModule()).Build()
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return app.Startup(ctx)
}
