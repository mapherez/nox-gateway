// Package command wires startup dependencies without global process state.
package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/mapherez/nox-gateway/internal/app"
	"github.com/mapherez/nox-gateway/internal/buildinfo"
	"github.com/mapherez/nox-gateway/internal/config"
	"github.com/mapherez/nox-gateway/internal/gateway"
	"github.com/mapherez/nox-gateway/internal/logging"
	"github.com/mapherez/nox-gateway/internal/state"
	"github.com/mapherez/nox-gateway/internal/transport"
	"github.com/mapherez/nox-gateway/internal/trust"
)

func Run(ctx context.Context, args []string, lookup config.LookupEnv, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("nox-gateway", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	operation := flags.String("service-operation", "", "offline maintenance: rotate, revoke-credential, revoke-service")
	health := flags.Bool("healthcheck", false, "check process liveness without opening state")
	version := flags.Bool("version", false, "print build version and exit")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, "Usage: nox-gateway [--version] [--healthcheck] [--help] [--service-operation=rotate|revoke-credential|revoke-service]\nConfiguration: NOX_GATEWAY_* environment variables. See .env.example and README.md.")
		return nil
	}
	if err != nil || flags.NArg() != 0 || (*health && (*version || *operation != "")) || (*operation != "" && (*version || (*operation != "rotate" && *operation != "revoke-credential" && *operation != "revoke-service"))) {
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
	if *health {
		return healthcheck(ctx, cfg)
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
	tlsConfig, err := transport.ServerTLS(cfg)
	if err != nil {
		return err
	}
	declaration, err := app.Declaration(buildinfo.Version)
	if err != nil {
		return errors.New("invalid Gateway build declaration")
	}
	engine, err := trust.New(cfg, store, declaration, logger)
	if err != nil {
		return err
	}
	defer engine.Close()
	if *operation != "" {
		if err = engine.Operate(ctx, *operation); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "{\"serviceConnection\":%q}\n", engine.Status())
		return nil
	}
	router, err := gateway.New(cfg.SocketDir, store.Identity().ServerID, buildinfo.Version, engine, store, gateway.Limits{CallTimeout: cfg.CallTimeout, ProbeInterval: cfg.ProbeInterval, MaxConcurrent: cfg.MaxConcurrent, MaxPayloadBytes: cfg.MaxPayloadBytes})
	if err != nil {
		return err
	}
	server, err := app.New(buildinfo.Version, logger, app.Options{PublicURL: cfg.PublicURL, TLSConfig: tlsConfig, ConnectionStatus: engine.Status, MCPHandler: router, Ready: router.Ready})
	if err != nil {
		return err
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	localDone := make(chan struct{})
	close(localDone)
	if cfg.SocketDir != "" {
		local, cleanup, e := transport.Listen(cfg.SocketDir, buildinfo.ProductID)
		if e != nil {
			return e
		}
		defer cleanup()
		localServer := &http.Server{BaseContext: func(net.Listener) context.Context { return work }, Handler: server.LocalHandler(engine.Receive), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
		defer localServer.Close()
		localDone = make(chan struct{})
		go func() {
			defer close(localDone)
			if e := localServer.Serve(local); e != nil && !errors.Is(e, http.ErrServerClosed) {
				cancel()
			}
		}()
		go func() {
			<-work.Done()
			shutdown, stop := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer stop()
			localServer.Shutdown(shutdown)
		}()
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ListenAddr)
	if err != nil {
		return errors.New("cannot bind HTTP listener; check NOX_GATEWAY_LISTEN_ADDR and port availability")
	}
	logger.Info("gateway_listening", "address", listener.Addr().String(), "version", buildinfo.Version, "contractVersion", buildinfo.ContractVersion)
	done := make(chan struct{})
	go func() { defer close(done); router.Run(work) }()
	err = server.Serve(work, listener, cfg.ShutdownTimeout)
	cancel()
	<-done
	<-localDone
	if err != nil {
		logger.Error("gateway_shutdown_failed")
		return err
	}
	logger.Info("gateway_stopped")
	return nil
}
