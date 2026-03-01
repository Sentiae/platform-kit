// Package config provides a Viper-based configuration loader that reads from
// environment variables with sensible defaults. Services use this package to
// load and validate their configuration structs.
//
// Usage:
//
//	type AppConfig struct {
//	    Port string `mapstructure:"port" validate:"required"`
//	    Env  string `mapstructure:"environment" validate:"required,oneof=development staging production"`
//	}
//
//	var cfg AppConfig
//	err := config.Load(&cfg, config.Options{
//	    EnvPrefix: "APP",
//	    Defaults:  map[string]any{"port": "8080", "environment": "development"},
//	})
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Options configures the config loader.
type Options struct {
	// EnvPrefix is the prefix for environment variables (e.g. "APP" → APP_PORT).
	EnvPrefix string

	// Defaults is a map of default values keyed by config path (e.g. "port" → "8080").
	Defaults map[string]any

	// ConfigFile is an optional path to a config file (YAML, JSON, TOML).
	// If empty, only env vars and defaults are used.
	ConfigFile string

	// ConfigPaths are directories to search for config files.
	ConfigPaths []string
}

// Load populates the target struct from environment variables, config files,
// and defaults. The target must be a pointer to a struct with `mapstructure` tags.
func Load(target any, opts Options) error {
	v := viper.New()

	// Set defaults.
	for key, value := range opts.Defaults {
		v.SetDefault(key, value)
	}

	// Configure env var reading.
	if opts.EnvPrefix != "" {
		v.SetEnvPrefix(opts.EnvPrefix)
	}
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Read config file if specified.
	if opts.ConfigFile != "" {
		v.SetConfigFile(opts.ConfigFile)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("reading config file: %w", err)
		}
	} else if len(opts.ConfigPaths) > 0 {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		for _, path := range opts.ConfigPaths {
			v.AddConfigPath(path)
		}
		// Config file is optional when paths are specified.
		_ = v.ReadInConfig()
	}

	// Unmarshal into the target struct.
	if err := v.Unmarshal(target); err != nil {
		return fmt.Errorf("unmarshaling config: %w", err)
	}

	return nil
}

// MustLoad calls Load and panics on error. Useful for application startup
// where a missing or invalid config is fatal.
func MustLoad(target any, opts Options) {
	if err := Load(target, opts); err != nil {
		panic(fmt.Sprintf("config: %v", err))
	}
}
