package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"github.com/BurntSushi/toml"
)

// Load resolves the configuration in three layers:
//
//  1. PRD §11 defaults from Default().
//  2. TOML at path, if it exists. A missing file is not an error — the daemon
//     runs on defaults until the user drops one in.
//  3. Environment-variable overrides for the small operational set (KSWP_*).
//
// The result is validated before return.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		if err := decodeTOML(path, &cfg); err != nil {
			return Config{}, err
		}
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func decodeTOML(path string, cfg *Config) error {
	f, err := os.Open(path) // #nosec G304 -- path is an operator-controlled config location
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open config %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	md, err := toml.NewDecoder(f).Decode(cfg)
	if err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return fmt.Errorf("unknown keys in %s: %v", path, undecoded)
	}
	return nil
}

// applyEnv overrides a small, deliberate set of fields. TOML covers everything
// else — we don't reflect over the struct to keep this layer explicit.
func applyEnv(cfg *Config) error {
	if v, ok := os.LookupEnv("KSWP_LOG_LEVEL"); ok {
		cfg.Daemon.LogLevel = v
	}
	if v, ok := os.LookupEnv("KSWP_HTTP_PORT"); ok {
		p, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("KSWP_HTTP_PORT: %w", err)
		}
		cfg.Daemon.HTTPPort = p
	}
	if v, ok := os.LookupEnv("KSWP_HTTP_TOKEN"); ok {
		cfg.Daemon.HTTPToken = v
	}
	if v, ok := os.LookupEnv("KSWP_ARGO_URL"); ok {
		cfg.Argo.URL = v
	}
	if v, ok := os.LookupEnv("KSWP_ARGO_TOKEN"); ok {
		cfg.Argo.Token = v
	}
	return nil
}
