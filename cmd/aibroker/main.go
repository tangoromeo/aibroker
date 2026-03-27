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
	"strings"
	"syscall"

	"aibroker/internal/config"
	"aibroker/internal/middleware"
	"aibroker/internal/proxy"
	"aibroker/internal/transport"
)

func main() {
	configPath := flag.String("config", "configs/aibroker.yaml", "path to YAML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "path", *configPath, "err", err)
		os.Exit(1)
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	front := transport.NewStdioTransport(os.Stdin, os.Stdout)
	back, err := transport.NewProcessTransport(ctx, cfg.Agent.Command, cfg.Agent.Args, cfg.Agent.Env)
	if err != nil {
		logger.Error("start agent process", "err", err)
		os.Exit(1)
	}

	mws, err := buildPipeline(cfg, logger)
	if err != nil {
		_ = back.Close()
		logger.Error("build pipeline", "err", err)
		os.Exit(1)
	}

	chain := proxy.Compose(mws...)
	p := proxy.NewProxy(front, back, chain, logger)

	logger.Info("aibroker started", "config", *configPath)

	err = p.Run(ctx)
	switch {
	case err == nil:
		logger.Info("aibroker stopped")
	case errors.Is(err, context.Canceled), errors.Is(err, io.EOF):
		logger.Info("aibroker stopped")
	default:
		logger.Error("aibroker exited", "err", err)
		os.Exit(1)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := parseLogLevel(cfg.Log.Level)
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch cfg.Log.Format {
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	default:
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func buildPipeline(cfg *config.Config, logger *slog.Logger) ([]proxy.Middleware, error) {
	var mws []proxy.Middleware
	for _, stage := range cfg.Pipeline {
		switch stage.Middleware {
		case "logging":
			mws = append(mws, middleware.Logging(logger))
		default:
			return nil, fmt.Errorf("unknown middleware %q in stage %q", stage.Middleware, stage.Name)
		}
	}
	return mws, nil
}
