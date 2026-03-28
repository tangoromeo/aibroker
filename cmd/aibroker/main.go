package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"aibroker/internal/broker"
	"aibroker/internal/config"
	"aibroker/internal/httpproxy"
	mw "aibroker/internal/middleware"
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

	switch cfg.Mode {
	case "http":
		runHTTP(ctx, cfg, logger)
	case "stdio":
		runStdio(ctx, cfg, logger)
	}
}

func runHTTP(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	// Build broker registry once — individual steps are resolved by name in the pipeline.
	var brokerReg *broker.Registry
	if cfg.Broker != nil {
		brokerCfg := broker.Config{
			EscalationMode: cfg.Broker.EscalationMode,
			StubDir:        cfg.Broker.StubDir,
			ScreeningLLM: broker.LLMEndpoint{
				URL:     cfg.Broker.Screening.URL,
				Model:   cfg.Broker.Screening.Model,
				APIKey:  cfg.Broker.Screening.APIKey,
				Timeout: cfg.Broker.Screening.Timeout,
				Headers: cfg.Broker.Screening.Headers,
			},
			ExternalLLM: broker.LLMEndpoint{
				URL:     cfg.Broker.Escalation.URL,
				Model:   cfg.Broker.Escalation.Model,
				APIKey:  cfg.Broker.Escalation.APIKey,
				Timeout: cfg.Broker.Escalation.Timeout,
				Headers: cfg.Broker.Escalation.Headers,
			},
			MinFailures: cfg.Broker.MinFailures,
		}
		for _, pd := range cfg.Broker.Policies {
			brokerCfg.Policies = append(brokerCfg.Policies, broker.PolicyConfig{
				Name:        pd.Name,
				Severity:    pd.Severity,
				Action:      pd.Action,
				Description: pd.Description,
				Prompt:      pd.Prompt,
			})
		}
		brokerReg = broker.Build(brokerCfg, logger)
		logger.Info("broker registered",
			"steps", brokerReg.Names(),
			"escalation_mode", cfg.Broker.EscalationMode,
			"policies", len(cfg.Broker.Policies),
		)
	}

	var mws []httpproxy.Middleware
	for _, stage := range cfg.Pipeline {
		switch stage.Middleware {
		case "logging":
			mws = append(mws, httpproxy.Logging(logger))
		case "dump":
			dir := "dumps"
			if v, ok := stage.Config["dir"].(string); ok {
				dir = v
			}
			mws = append(mws, httpproxy.RequestDump(dir, logger))
		case "tool_adapter":
			mws = append(mws, httpproxy.ToolAdapter(logger))
		case "context_trim":
			trimCfg := httpproxy.ContextTrimConfig{}
			if v, ok := stage.Config["max_tokens"].(int); ok {
				trimCfg.MaxTokens = v
			}
			if v, ok := stage.Config["system_max_tokens"].(int); ok {
				trimCfg.SystemMaxTokens = v
			}
			if v, ok := stage.Config["preserve_last_n"].(int); ok {
				trimCfg.PreserveLastN = v
			}
			mws = append(mws, httpproxy.ContextTrim(trimCfg, logger))
		default:
			// Try broker registry (escalation_detect, escalation_screen, etc.)
			if m := brokerReg.Get(stage.Middleware); m != nil {
				mws = append(mws, m)
				logger.Info("pipeline: added broker step", "name", stage.Middleware)
			} else {
				logger.Error("unknown middleware", "name", stage.Middleware)
				os.Exit(1)
			}
		}
	}

	var pipeline httpproxy.Middleware
	if len(mws) > 0 {
		pipeline = httpproxy.Compose(mws...)
	}

	httpClient, err := buildHTTPClient(cfg)
	if err != nil {
		logger.Error("configure TLS", "err", err)
		os.Exit(1)
	}

	p := httpproxy.New(httpproxy.Config{
		Upstream:   cfg.Upstream.URL,
		APIKey:     cfg.Upstream.APIKey,
		Timeout:    cfg.Upstream.Timeout,
		Headers:    cfg.Upstream.Headers,
		HTTPClient: httpClient,
	}, pipeline, logger)

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: p,
	}

	go func() {
		<-ctx.Done()
		logger.Info("shutting down")
		_ = srv.Close()
	}()

	logger.Info("aibroker http proxy started",
		"listen", cfg.Listen,
		"upstream", cfg.Upstream.URL,
	)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
	logger.Info("aibroker stopped")
}

func runStdio(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	front := transport.NewStdioTransport(os.Stdin, os.Stdout)
	back, err := transport.NewProcessTransport(ctx, cfg.Agent.Command, cfg.Agent.Args, cfg.Agent.Env)
	if err != nil {
		logger.Error("start agent process", "err", err)
		os.Exit(1)
	}

	mws, err := buildStdioPipeline(cfg, logger)
	if err != nil {
		_ = back.Close()
		logger.Error("build pipeline", "err", err)
		os.Exit(1)
	}

	chain := proxy.Compose(mws...)
	p := proxy.NewProxy(front, back, chain, logger)

	logger.Info("aibroker stdio proxy started")

	err = p.Run(ctx)
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, io.EOF):
		logger.Info("aibroker stopped")
	default:
		logger.Error("aibroker exited", "err", err)
		os.Exit(1)
	}
}

func buildStdioPipeline(cfg *config.Config, logger *slog.Logger) ([]proxy.Middleware, error) {
	var mws []proxy.Middleware
	for _, stage := range cfg.Pipeline {
		switch stage.Middleware {
		case "logging":
			mws = append(mws, mw.Logging(logger))
		default:
			return nil, fmt.Errorf("unknown middleware %q in stage %q", stage.Middleware, stage.Name)
		}
	}
	return mws, nil
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

func buildHTTPClient(cfg *config.Config) (*http.Client, error) {
	tlsCfg := &tls.Config{}
	custom := false

	if cfg.Upstream.TLS.Insecure {
		tlsCfg.InsecureSkipVerify = true
		custom = true
	}

	if cfg.Upstream.TLS.CACert != "" {
		caCert, err := os.ReadFile(cfg.Upstream.TLS.CACert)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("invalid CA cert: %s", cfg.Upstream.TLS.CACert)
		}
		tlsCfg.RootCAs = pool
		custom = true
	}

	if cfg.Upstream.TLS.ClientCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.Upstream.TLS.ClientCert, cfg.Upstream.TLS.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("load client cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
		custom = true
	}

	if !custom {
		return nil, nil
	}

	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}
