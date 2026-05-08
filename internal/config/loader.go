package config

import (
	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"
)

// Config is the typed shape of ~/.config/wiki-audio/config.toml.
// Fleshed out in Phase D — see wa-kyn.* beads.
type Config struct{}

// Load reads config.toml from path and overlays env vars from envPath.
// Stub: not yet implemented (filled in by wa-kyn.* / Phase D).
func Load(configPath, envPath string) (*Config, error) {
	_ = toml.MetaData{}
	_ = godotenv.Load
	return nil, errNotImplemented
}

type errStr string

func (e errStr) Error() string { return string(e) }

const errNotImplemented = errStr("config.Load: not yet implemented")
