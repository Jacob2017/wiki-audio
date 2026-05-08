package cli

// Version is injected at build time via -ldflags
// "-X github.com/Jacob2017/wiki-audio/internal/cli.Version=...".
// Defaults to "dev" so unbuilt/local runs are obvious. See wa-8gt.4.
var Version = "dev"
