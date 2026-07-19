package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"project/src/core/app"
	"project/src/server/gate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if err := gate.LoadConfigs(); err != nil {
		return fmt.Errorf("load configs: %w", err)
	}

	// 从环境变量读取节点 ID，默认 "1.1.0"
	nodeID := os.Getenv("NODE_ID")
	if nodeID == "" {
		nodeID = "1.1.0"
	}

	app, err := app.NewBuilder(
		app.WithName("gatesvr"),
		app.WithNodeID(nodeID),
	).AddModule(gate.NewModule()).Build()
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return app.Startup(ctx)
}
