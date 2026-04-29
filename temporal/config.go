package temporal

// EnvConfig is a Viper-friendly config block. Mount as `Temporal` in service
// config; mapstructure tags pick up APP_TEMPORAL_HOSTPORT, APP_TEMPORAL_NAMESPACE.
type EnvConfig struct {
	HostPort  string `mapstructure:"hostport" validate:"required"`
	Namespace string `mapstructure:"namespace"`
	Identity  string `mapstructure:"identity"`
}

// ToConfig converts EnvConfig to runtime Config (logger injected separately).
func (e EnvConfig) ToConfig() Config {
	return Config{
		HostPort:  e.HostPort,
		Namespace: e.Namespace,
		Identity:  e.Identity,
	}
}
