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
//
// Load applies the `validate` tags after populating the target and returns a
// *ValidationError naming each offending field, so a service with absent or
// invalid config fails at boot rather than running degraded. A struct with no
// `validate` tags is unaffected.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

// validate is the shared struct validator applied to every Load target.
// WithRequiredStructEnabled matches platform-kit's own validation package and
// makes `required` on a nested struct field meaningful rather than a silent
// no-op.
var validate = validator.New(validator.WithRequiredStructEnabled())

// ValidationError reports config fields that failed their `validate` tag.
// It is returned by Load when the populated target violates its own rules,
// so a service fails fast at boot instead of running on absent config.
type ValidationError struct {
	// Fields names each offending field as "Struct.Field: reason".
	Fields []string
}

func (e *ValidationError) Error() string {
	return "config validation failed: " + strings.Join(e.Fields, "; ")
}

// validateTarget runs the struct validator against target. Targets that are not
// structs (e.g. a map) cannot carry `validate` tags, so they are skipped.
func validateTarget(target any) error {
	rv := reflect.ValueOf(target)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return errors.New("config: target is nil")
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}

	err := validate.Struct(rv.Interface())
	if err == nil {
		return nil
	}

	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return fmt.Errorf("validating config: %w", err)
	}

	fields := make([]string, 0, len(verrs))
	for _, fe := range verrs {
		if fe.Param() == "" {
			fields = append(fields, fmt.Sprintf("%s: failed %q", fe.Namespace(), fe.Tag()))
			continue
		}
		fields = append(fields, fmt.Sprintf("%s: failed %q (%s)", fe.Namespace(), fe.Tag(), fe.Param()))
	}
	return &ValidationError{Fields: fields}
}

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

	// BindEnvs is a list of explicit [configPath, envVarName] bindings for nested
	// config fields where viper's AutomaticEnv doesn't resolve during Unmarshal.
	// Example: [][2]string{{"database.host", "APP_DATABASE_HOST"}}
	BindEnvs [][2]string

	// Profile selects a per-environment override file. When set (or when the
	// APP_PROFILE / <EnvPrefix>_PROFILE env var is set), the loader first reads
	// the base config.yaml and then layers config.<profile>.yaml on top so
	// profile-specific values override the base. When empty the default
	// profile is "dev". Unknown profiles are treated as valid (the override
	// file is optional — a missing profile file is not an error).
	//
	// Precedence (lowest → highest):
	//   defaults → ConfigFile/ConfigPaths base → profile override → env vars
	Profile string

	// OnReload, if non-nil, starts a file watcher on ConfigFile (or the first
	// resolved file under ConfigPaths) and invokes the callback on change. The
	// callback should re-call Load with the same target to refresh live values.
	// Only values safe to change at runtime (log levels, feature toggles,
	// non-secret strings) should be read via hot-reload; secrets, connection
	// pools, and socket bindings still require a restart.
	OnReload func()

	// ReloadDebounce rate-limits reload firings. Defaults to 2s when zero.
	ReloadDebounce time.Duration
}

// DefaultProfile is the profile applied when none is specified.
const DefaultProfile = "dev"

// validProfiles lists the recognized first-class environments. A profile
// outside this set is still accepted (its override file is optional) so
// services can define custom profiles (e.g. "canary") without code changes.
var validProfiles = map[string]bool{
	"dev":     true,
	"staging": true,
	"prod":    true,
}

// IsKnownProfile reports whether the given profile is a first-class, known
// environment. Unknown profiles are still loadable; this is advisory.
func IsKnownProfile(profile string) bool {
	return validProfiles[profile]
}

// resolveProfile picks the effective profile name from (in order):
//  1. opts.Profile
//  2. <EnvPrefix>_PROFILE env var (e.g. APP_PROFILE)
//  3. APP_PROFILE (always checked as a fallback so a single global var works
//     across services even when they use different EnvPrefix values)
//  4. DefaultProfile
func resolveProfile(opts Options) string {
	if opts.Profile != "" {
		return opts.Profile
	}
	if opts.EnvPrefix != "" {
		if v := os.Getenv(opts.EnvPrefix + "_PROFILE"); v != "" {
			return v
		}
	}
	if v := os.Getenv("APP_PROFILE"); v != "" {
		return v
	}
	return DefaultProfile
}

// profileOverrideFile returns "config.<profile>.yaml" alongside baseFile, or
// "" when baseFile is empty.
func profileOverrideFile(baseFile, profile string) string {
	if baseFile == "" || profile == "" {
		return ""
	}
	dir, name := filepath.Split(baseFile)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if ext == "" {
		ext = ".yaml"
	}
	return filepath.Join(dir, stem+"."+profile+ext)
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

	// Bind explicit env vars for nested fields.
	for _, b := range opts.BindEnvs {
		_ = v.BindEnv(b[0], b[1])
	}

	// Resolve the effective profile (dev by default). Profile overrides are
	// layered on top of the base file so nested keys merge naturally.
	profile := resolveProfile(opts)

	// Read config file if specified.
	baseFileUsed := ""
	if opts.ConfigFile != "" {
		v.SetConfigFile(opts.ConfigFile)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("reading config file: %w", err)
		}
		baseFileUsed = opts.ConfigFile
	} else if len(opts.ConfigPaths) > 0 {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		for _, path := range opts.ConfigPaths {
			v.AddConfigPath(path)
		}
		// Config file is optional when paths are specified.
		_ = v.ReadInConfig()
		baseFileUsed = v.ConfigFileUsed()
	}

	// Layer the profile-specific override file on top of the base. MergeInConfig
	// preserves any keys not defined in the override, so partial profile files
	// are supported. A missing override file is not an error.
	if baseFileUsed != "" && profile != "" {
		overridePath := profileOverrideFile(baseFileUsed, profile)
		if overridePath != "" {
			if _, statErr := os.Stat(overridePath); statErr == nil {
				v.SetConfigFile(overridePath)
				if err := v.MergeInConfig(); err != nil {
					return fmt.Errorf("merging profile %q from %s: %w", profile, overridePath, err)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("stat profile file %s: %w", overridePath, statErr)
			}
		}
	}

	// Unmarshal into the target struct.
	if err := v.Unmarshal(target); err != nil {
		return fmt.Errorf("unmarshaling config: %w", err)
	}

	// Validate the populated target against its `validate` tags (§17: validate
	// on Load, fail fast). Runs before the hot-reloader starts so an invalid
	// config never leaves a watcher goroutine behind.
	if err := validateTarget(target); err != nil {
		return err
	}

	if opts.OnReload != nil {
		watchPath := opts.ConfigFile
		if watchPath == "" {
			watchPath = v.ConfigFileUsed()
		}
		if watchPath != "" {
			reloader, err := NewHotReloader([]string{watchPath}, opts.OnReload, opts.ReloadDebounce)
			if err != nil {
				return fmt.Errorf("starting hot-reloader: %w", err)
			}
			go reloader.Start()
		}
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
