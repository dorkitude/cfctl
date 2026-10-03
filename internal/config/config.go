package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	configDirEnv   = "CFCTL_CONFIG_DIR"
	tokenFileName  = "token"
	configFileName = "config.yaml"

	// Viper keys.
	KeyAccountID  = "account_id"
	KeyToken      = "token"
	KeyAPIBaseURL = "api_base_url"
)

// Token environment variables, in priority order. Both override the token file.
var tokenEnvVars = []string{"CFCTL_TOKEN", "CLOUDFLARE_API_TOKEN"}

var v = newViper()

// Config holds the persisted configuration for cfctl.
// The token is never part of it: it lives in its own 0600 file.
type Config struct {
	AccountID string `mapstructure:"account_id"`
}

// newViper builds a Viper instance with cfctl's env bindings.
//
// Precedence (highest first): --account flag, env vars, config.yaml.
func newViper() *viper.Viper {
	nv := viper.New()
	nv.SetConfigType("yaml")
	_ = nv.BindEnv(KeyAccountID, "CFCTL_ACCOUNT_ID")
	_ = nv.BindEnv(append([]string{KeyToken}, tokenEnvVars...)...)
	_ = nv.BindEnv(KeyAPIBaseURL, "CFCTL_API_BASE_URL")
	return nv
}

// Init (re)loads configuration from env vars and config.yaml, and binds the
// --account flag if given. Called once per command invocation.
func Init(accountFlag *pflag.Flag) error {
	v = newViper()
	if accountFlag != nil {
		if err := v.BindPFlag(KeyAccountID, accountFlag); err != nil {
			return err
		}
	}
	dir, err := resolveConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, configFileName)
	v.SetConfigFile(path)
	if _, err := os.Stat(path); err == nil {
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}
	}
	return nil
}

func defaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cfctl"), nil
}

func cleanConfigPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("config directory cannot be empty")
	}

	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}

	return filepath.Clean(path), nil
}

func resolveConfigDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv(configDirEnv)); env != "" {
		return cleanConfigPath(env)
	}
	return defaultConfigDir()
}

// ResolveConfigDir returns the resolved config directory path without creating it.
func ResolveConfigDir() (string, error) {
	return resolveConfigDir()
}

// configDir returns the resolved config directory, creating it (0700) if needed.
func configDir() (string, error) {
	dir, err := resolveConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	// MkdirAll leaves an existing dir's mode alone; tighten it.
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// TokenPath returns the path to the token file (without creating anything).
func TokenPath() (string, error) {
	dir, err := resolveConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, tokenFileName), nil
}

// ConfigPath returns the path to config.yaml (without creating anything).
func ConfigPath() (string, error) {
	dir, err := resolveConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// TokenEnvVar returns the name of the env var supplying the token, or "".
func TokenEnvVar() string {
	for _, name := range tokenEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return name
		}
	}
	return ""
}

// LoadToken returns the API token: env var first, then the token file.
func LoadToken() (string, error) {
	if t := strings.TrimSpace(v.GetString(KeyToken)); t != "" {
		return t, nil
	}
	path, err := TokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("not authenticated — run 'cfctl auth login' first")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty — run 'cfctl auth login'")
	}
	return token, nil
}

// SaveToken writes the API token to disk with 0600 permissions (dir 0700).
func SaveToken(token string) error {
	if _, err := configDir(); err != nil {
		return err
	}
	path, err := TokenPath()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		return err
	}
	// WriteFile keeps the mode of an existing file; tighten it.
	return os.Chmod(path, 0600)
}

// RemoveToken deletes the stored token.
func RemoveToken() error {
	path, err := TokenPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(path)
}

// HasToken returns true if a token file exists.
func HasToken() bool {
	path, err := TokenPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// AccountID returns the effective account ID (flag > env > config.yaml).
func AccountID() string {
	return strings.TrimSpace(v.GetString(KeyAccountID))
}

// APIBaseURL returns an API base URL override, or "" for the default.
// Used by tests to point cfctl at a fake server.
func APIBaseURL() string {
	return strings.TrimSpace(v.GetString(KeyAPIBaseURL))
}

// Load reads the persisted config (config.yaml only; no flags or env).
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	fv := viper.New()
	fv.SetConfigFile(path)
	fv.SetConfigType("yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err := fv.ReadInConfig(); err != nil {
		return nil, err
	}
	var cfg Config
	if err := fv.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes the config to config.yaml (0600).
//
// It uses a fresh Viper instance on purpose: writing the shared instance would
// also persist env-bound values, including a token from CLOUDFLARE_API_TOKEN.
func Save(cfg *Config) error {
	if _, err := configDir(); err != nil {
		return err
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	fv := viper.New()
	fv.SetConfigType("yaml")
	fv.Set(KeyAccountID, cfg.AccountID)
	if err := fv.WriteConfigAs(path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	v.Set(KeyAccountID, cfg.AccountID)
	return nil
}
