// Package config loads ~/.wiki-audio/config.toml and the companion
// .env file via godotenv (§3, §8.6). Paths are overridable by the
// --config and --env persistent flags on the root command, and
// --env-local overrides the .env path to the current working
// directory (useful during development).
package config
