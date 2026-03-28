package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode     string         `yaml:"mode"` // "http" or "stdio"
	Listen   string         `yaml:"listen"`
	Upstream UpstreamConfig `yaml:"upstream"`
	Broker   *BrokerConfig  `yaml:"broker"`
	Agent    AgentConfig    `yaml:"agent"`
	Pipeline []StageConfig  `yaml:"pipeline"`
	Log      LogConfig      `yaml:"log"`
}

type BrokerConfig struct {
	MinFailures     int               `yaml:"min_failures"`
	ForceEscalation bool              `yaml:"force_escalation"`
	Screening       LLMEndpointConfig `yaml:"screening"`
	Escalation      LLMEndpointConfig `yaml:"escalation"`
	EscalationMode  string            `yaml:"escalation_mode"` // "stub" or "live"
	StubDir         string            `yaml:"stub_dir"`
	Policies        []PolicyDef       `yaml:"policies"`
}

type LLMEndpointConfig struct {
	URL     string            `yaml:"url"`
	Model   string            `yaml:"model"`
	APIKey  string            `yaml:"api_key"`
	Timeout time.Duration     `yaml:"timeout"`
	Headers map[string]string `yaml:"headers"`
}

type PolicyDef struct {
	Name        string `yaml:"name"`
	Severity    string `yaml:"severity"`
	Action      string `yaml:"action"`
	Description string `yaml:"description"`
	Prompt      string `yaml:"prompt"`
}

type UpstreamConfig struct {
	URL       string            `yaml:"url"`
	APIKey    string            `yaml:"api_key"`
	Timeout   time.Duration     `yaml:"timeout"`
	TLS       TLSConfig         `yaml:"tls"`
	Headers   map[string]string `yaml:"headers"`
}

type TLSConfig struct {
	Insecure     bool   `yaml:"insecure"`       // skip TLS verification (testing only)
	CACert       string `yaml:"ca_cert"`         // path to custom CA certificate
	ClientCert   string `yaml:"client_cert"`     // path to client certificate
	ClientKey    string `yaml:"client_key"`      // path to client certificate key
}

type AgentConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []string `yaml:"env"`
}

type StageConfig struct {
	Name       string         `yaml:"name"`
	Middleware string         `yaml:"middleware"`
	Config     map[string]any `yaml:"config"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Expand ${VAR} and $VAR references from environment.
	data = []byte(os.ExpandEnv(string(data)))

	cfg := &Config{
		Mode:   "http",
		Listen: ":8080",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	return cfg, validate(cfg)
}

func applyDefaults(cfg *Config) {
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "text"
	}
	cfg.Log.Level = strings.ToLower(cfg.Log.Level)
	cfg.Log.Format = strings.ToLower(cfg.Log.Format)
	if cfg.Upstream.Timeout == 0 {
		cfg.Upstream.Timeout = 5 * time.Minute
	}
	if cfg.Broker != nil {
		if cfg.Broker.MinFailures <= 0 {
			cfg.Broker.MinFailures = 3
		}
		if cfg.Broker.Screening.Timeout == 0 {
			cfg.Broker.Screening.Timeout = 60 * time.Second
		}
		if cfg.Broker.Escalation.Timeout == 0 {
			cfg.Broker.Escalation.Timeout = 2 * time.Minute
		}
	}
}

func validate(cfg *Config) error {
	switch cfg.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("config: log.format must be text or json, got %q", cfg.Log.Format)
	}
	switch cfg.Mode {
	case "http":
		if cfg.Upstream.URL == "" {
			return fmt.Errorf("config: upstream.url is required in http mode")
		}
	case "stdio":
		if cfg.Agent.Command == "" {
			return fmt.Errorf("config: agent.command is required in stdio mode")
		}
	default:
		return fmt.Errorf("config: mode must be http or stdio, got %q", cfg.Mode)
	}
	return nil
}
