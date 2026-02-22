package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/revrost/elok/pkg/agent"
	"github.com/revrost/elok/pkg/channels"
	"github.com/revrost/elok/pkg/config"
	"github.com/revrost/elok/pkg/gateway"
	"github.com/revrost/elok/pkg/llm"
	"github.com/revrost/elok/pkg/observability"
	"github.com/revrost/elok/pkg/plugins"
	"github.com/revrost/elok/pkg/store"
	"github.com/revrost/elok/pkg/tools"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("elok failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runCommand(args)
	}
	switch args[0] {
	case "run":
		return runCommand(args[1:])
	case "migrate":
		return migrateCommand(args[1:])
	case "init-config":
		return initConfigCommand(args[1:])
	case "version":
		fmt.Printf("elok %s\n", version)
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", config.DefaultConfigPath(), "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log, logCloser, err := observability.NewLogger(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if logCloser == nil {
			return
		}
		if closeErr := logCloser.Close(); closeErr != nil {
			_, _ = io.WriteString(os.Stderr, "failed to close log exporter: "+closeErr.Error()+"\n")
		}
	}()
	slog.SetDefault(log)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	pluginManager := plugins.NewManager(log)
	if err := pluginManager.Start(ctx, cfg.Plugins); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = pluginManager.Stop(shutdownCtx)
	}()

	agentSvc := agent.NewService(st, llm.New(cfg.LLM), pluginManager, tools.NewRegistry(), log)

	channelManager := channels.NewManager(log, func(ctx context.Context, sessionID, text string) (string, error) {
		result, err := agentSvc.Send(ctx, sessionID, text)
		if err != nil {
			return "", err
		}
		return result.AssistantText, nil
	})
	if err := channelManager.Start(ctx, cfg); err != nil {
		return err
	}
	defer func() {
		if err := channelManager.Close(); err != nil {
			log.Warn("channel manager close failed", "error", err)
		}
	}()

	gatewayServer := gateway.NewServer(cfg.ListenAddr, agentSvc, channelManager, log)

	err = gatewayServer.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func migrateCommand(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := fs.String("config", config.DefaultConfigPath(), "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	fmt.Println("migrations applied")
	return nil
}

func initConfigCommand(args []string) error {
	fs := flag.NewFlagSet("init-config", flag.ContinueOnError)
	path := fs.String("path", config.DefaultConfigPath(), "output config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := config.Save(*path, config.Default()); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", *path)
	return nil
}
