package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mapherez/nox-gateway/internal/command"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command.Run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); err != nil {
		// Command errors are deliberately safe, fixed descriptions. Never print
		// the environment, arguments, credentials, or raw HTTP/storage errors.
		fmt.Fprintln(os.Stderr, "nox-gateway:", err)
		os.Exit(1)
	}
}
