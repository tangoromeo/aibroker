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
	Rules       string `yaml:"rules"`
}

type UpstreamConfig struct {
	URL            string               `yaml:"url"`
	APIKey         string               `yaml:"api_key"`
	Timeout        time.Duration        `yaml:"timeout"`
	TLS            TLSConfig            `yaml:"tls"`
	Headers        map[string]string    `yaml:"headers"`
	AuthFromClient *AuthFromClientConfig `yaml:"auth_from_client"`
	ClientRouting  *ClientRoutingConfig  `yaml:"client_routing"`
}

// AuthFromClient reconstructs the upstream API key from the client request so the
// full secret does not need to live in config. The bearer token and extra_header
// value are concatenated (order set by join).
type AuthFromClientConfig struct {
	Enabled     bool   `yaml:"enabled"`
	ExtraHeader string `yaml:"extra_header"` // second part; required when enabled (Continue may override via client_routing)
	Join        string `yaml:"join"`         // bearer_first (default) or header_first
}

// ClientRouting selects per-client behavior. Continue is handled separately; all
// other clients (including Kilo) use the OpenAI-compatible path.
type ClientRoutingConfig struct {
	Continue ContinueRoutingConfig `yaml:"continue"`
}

// ContinueRoutingConfig matches Continue.dev (or compatible) clients.
type ContinueRoutingConfig struct {
	UserAgentSubstrings []string `yaml:"user_agent_substrings"`
	HeaderPresent       string   `yaml:"header_present"`
	// ExtraHeader overrides auth_from_client.extra_header for matched Continue clients.
	ExtraHeader string `yaml:"extra_header"`
	// ColonBearerSplit: Bearer token is "openai-key:some-id". Upstream gets Authorization
	// Bearer openai-key and some-id in IdHeader (Continue corporate backends often expect this).
	ColonBearerSplit *ColonBearerSplitConfig `yaml:"colon_bearer_split"`
}

// ColonBearerSplitConfig splits the Bearer token on the first ':'.
type ColonBearerSplitConfig struct {
	Enabled   bool   `yaml:"enabled"`
	IDHeader  string `yaml:"id_header"` // HTTP header name for the segment after ':'
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
		if cfg.Upstream.AuthFromClient != nil && cfg.Upstream.AuthFromClient.Enabled {
			colonOK := cfg.Upstream.ClientRouting != nil &&
				cfg.Upstream.ClientRouting.Continue.ColonBearerSplit != nil &&
				cfg.Upstream.ClientRouting.Continue.ColonBearerSplit.Enabled &&
				cfg.Upstream.ClientRouting.Continue.ColonBearerSplit.IDHeader != ""
			if cfg.Upstream.AuthFromClient.ExtraHeader == "" && !colonOK {
				return fmt.Errorf("config: upstream.auth_from_client.extra_header is required when auth_from_client.enabled (unless continue.colon_bearer_split is fully configured)")
			}
			if cfg.Upstream.AuthFromClient.ExtraHeader != "" {
				switch cfg.Upstream.AuthFromClient.Join {
				case "", "bearer_first", "header_first":
				default:
					return fmt.Errorf("config: upstream.auth_from_client.join must be bearer_first or header_first")
				}
			}
		}
		if cr := cfg.Upstream.ClientRouting; cr != nil && cr.Continue.ColonBearerSplit != nil && cr.Continue.ColonBearerSplit.Enabled {
			if cr.Continue.ColonBearerSplit.IDHeader == "" {
				return fmt.Errorf("config: client_routing.continue.colon_bearer_split.id_header is required when colon_bearer_split.enabled")
			}
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
