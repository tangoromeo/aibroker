package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Agent    AgentConfig   `yaml:"agent"`
	Pipeline []StageConfig `yaml:"pipeline"`
	Log      LogConfig     `yaml:"log"`
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
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "text"
	}
	cfg.Log.Level = strings.ToLower(cfg.Log.Level)
	cfg.Log.Format = strings.ToLower(cfg.Log.Format)
	switch cfg.Log.Format {
	case "text", "json":
	default:
		return nil, fmt.Errorf("config: log.format must be text or json, got %q", cfg.Log.Format)
	}
	return &cfg, nil
}
