// Package command wires startup dependencies without global process state.
package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/mapherez/nox-gateway/internal/app"
	"github.com/mapherez/nox-gateway/internal/buildinfo"
	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/logging"
	"github.com/mapherez/nox-gateway/internal/state"
)

func Run(ctx context.Context, args []string, lookup config.LookupEnv, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("nox-gateway", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	version := flags.Bool("version", false, "print build version and exit")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, "Usage: nox-gateway [--version] [--help]\nConfiguration: NOX_GATEWAY_* environment variables. See .env.example and README.md.")
		return nil
	}
	if err != nil || flags.NArg() != 0 {
		return errors.New("invalid command-line arguments; use --help")
	}
	if *version {
		fmt.Fprintf(stdout, "%s %s (Suite %s)\n", buildinfo.ProductID, buildinfo.Version, buildinfo.ContractVersion)
		return nil
	}
	cfg, err := config.Load(lookup)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	logger := logging.New(stderr, cfg.LogLevel)
	store, err := state.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ListenAddr)
	if err != nil {
		return errors.New("cannot bind HTTP listener; check NOX_GATEWAY_LISTEN_ADDR and port availability")
	}
	logger.Info("gateway_listening", "address", listener.Addr().String(), "version", buildinfo.Version, "contractVersion", buildinfo.ContractVersion)
	server := app.New(buildinfo.Version, logger)
	err = server.Serve(ctx, listener, cfg.ShutdownTimeout)
	if err != nil {
		logger.Error("gateway_shutdown_failed")
		return err
	}
	logger.Info("gateway_stopped")
	return nil
}
