package raptor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	CACertificate       string `yaml:"ca_certificate"`
	ClientCert          string `yaml:"client_cert"`
	ClientPrivateKey    string `yaml:"client_private_key"`
	APIConnectionString string `yaml:"api_connection_string"`
	PinnedServerName    string `yaml:"pinned_server_name"`
	MaxGRPCRecvSize     int    `yaml:"max_grpc_recv_size"`

	OrgID                string
	EnableDangerousTools bool
	DisabledTools        []string
	LogLevel             string
	LogFile              string
	LockFile             string
	MaxResponseBytes     int
	DefaultTimeout       time.Duration
}

func LoadConfig(path string) (*Config, error) {
	resolved, err := resolveConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", resolved, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", resolved, err)
	}

	applyEnvOverrides(&cfg)

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func resolveConfigPath(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if v := os.Getenv("VELOCIRAPTOR_API_CONFIG"); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, "./api_client.yaml")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "velociraptor", "api_client.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "velociraptor", "api_client.yaml"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no config file found; set --config or VELOCIRAPTOR_API_CONFIG")
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("VELOCIRAPTOR_ORG_ID"); v != "" {
		cfg.OrgID = v
	}
	if v := os.Getenv("ENABLE_DANGEROUS_TOOLS"); v == "true" {
		cfg.EnableDangerousTools = true
	}
	if v := os.Getenv("VELOCIRAPTOR_DISABLED_TOOLS"); v != "" {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				cfg.DisabledTools = append(cfg.DisabledTools, t)
			}
		}
	}
	if v := os.Getenv("RAPTOR_MAX_RESPONSE_BYTES"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.MaxResponseBytes)
	}
	if v := os.Getenv("RAPTOR_TIMEOUT_SECONDS"); v != "" {
		var secs int
		fmt.Sscanf(v, "%d", &secs)
		cfg.DefaultTimeout = time.Duration(secs) * time.Second
	}
	if v := os.Getenv("RAPTOR_LOG_FILE"); v != "" {
		cfg.LogFile = v
	}
	if v := os.Getenv("RAPTOR_LOCK_FILE"); v != "" {
		cfg.LockFile = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}

	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 32000
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 300 * time.Second
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "debug"
	}
	if cfg.PinnedServerName == "" {
		cfg.PinnedServerName = "VelociraptorServer"
	}
}

func (cfg *Config) validate() error {
	if cfg.CACertificate == "" {
		return fmt.Errorf("config: ca_certificate is required")
	}
	if cfg.ClientCert == "" {
		return fmt.Errorf("config: client_cert is required")
	}
	if cfg.ClientPrivateKey == "" {
		return fmt.Errorf("config: client_private_key is required")
	}
	if cfg.APIConnectionString == "" {
		return fmt.Errorf("config: api_connection_string is required")
	}
	return nil
}
