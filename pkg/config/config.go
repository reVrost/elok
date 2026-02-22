package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	defaultDirName        = ".elok"
	defaultConfigFileName = "config.toml"
)

type Config struct {
	DBPath     string         `toml:"db_path"`
	ListenAddr string         `toml:"listen_addr"`
	LLM        LLMConfig      `toml:"llm"`
	Plugins    PluginConfig   `toml:"plugins"`
	Gateway    GatewayConfig  `toml:"gateway"`
	WhatsApp   WhatsAppConfig `toml:"whatsapp"`
}

type GatewayConfig struct {
	EnableWS bool `toml:"enable_ws"`
}

type WhatsAppConfig struct {
	Enabled   bool   `toml:"enabled"`
	StorePath string `toml:"store_path"`
}

type LLMConfig struct {
	Provider      string `toml:"provider"`
	Model         string `toml:"model"`
	APIKeyEnv     string `toml:"api_key_env"`
	BaseURL       string `toml:"base_url"`
	CodexAuthPath string `toml:"codex_auth_path"`
}

type PluginConfig struct {
	Enabled bool         `toml:"enabled"`
	Entries []PluginSpec `toml:"entries"`
}

type PluginSpec struct {
	ID      string   `toml:"id"`
	Command []string `toml:"command"`
}

func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return defaultDirName
	}
	return filepath.Join(home, defaultDirName)
}

func DefaultConfigPath() string {
	return filepath.Join(DefaultDir(), defaultConfigFileName)
}

func ExpandPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if trimmed == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return trimmed
	}
	if strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return trimmed
		}
		return filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
	}
	return trimmed
}

func Default() Config {
	return Config{
		DBPath:     filepath.Join(DefaultDir(), "elok.db"),
		ListenAddr: "127.0.0.1:7777",
		LLM: LLMConfig{
			Provider:      "mock",
			Model:         "mock/default",
			APIKeyEnv:     "OPENROUTER_API_KEY",
			BaseURL:       "https://openrouter.ai/api/v1",
			CodexAuthPath: "~/.codex/auth.json",
		},
		Plugins: PluginConfig{
			Enabled: true,
			Entries: []PluginSpec{
				{
					ID:      "plan-mode",
					Command: []string{"go", "run", "./plugins/plan-mode/cmd/planmode"},
				},
			},
		},
		Gateway: GatewayConfig{EnableWS: true},
		WhatsApp: WhatsAppConfig{
			Enabled:   false,
			StorePath: filepath.Join(DefaultDir(), "whatsapp.db"),
		},
	}
}

func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfigPath()
	}
	path = ExpandPath(path)

	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return Config{}, fmt.Errorf("decode toml config: %w", err)
	}
	if cfg.DBPath == "" {
		cfg.DBPath = Default().DBPath
	}
	cfg.DBPath = ExpandPath(cfg.DBPath)
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = Default().ListenAddr
	}
	if cfg.LLM.Provider == "" {
		cfg.LLM.Provider = Default().LLM.Provider
	}
	if cfg.LLM.Model == "" {
		cfg.LLM.Model = Default().LLM.Model
	}
	if cfg.LLM.APIKeyEnv == "" {
		cfg.LLM.APIKeyEnv = Default().LLM.APIKeyEnv
	}
	if cfg.LLM.BaseURL == "" {
		cfg.LLM.BaseURL = Default().LLM.BaseURL
	}
	if cfg.LLM.CodexAuthPath == "" {
		cfg.LLM.CodexAuthPath = Default().LLM.CodexAuthPath
	}
	cfg.LLM.CodexAuthPath = ExpandPath(cfg.LLM.CodexAuthPath)
	if cfg.WhatsApp.StorePath == "" {
		cfg.WhatsApp.StorePath = Default().WhatsApp.StorePath
	}
	cfg.WhatsApp.StorePath = ExpandPath(cfg.WhatsApp.StorePath)
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfigPath()
	}
	path = ExpandPath(path)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode toml config: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (c LLMConfig) ResolveAPIKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}
