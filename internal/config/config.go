// Package config defines the Config struct and a config Loader interface. Implementations live in sub packages
package config

import "context"

type Config struct {
	LogLevel            string   `json:"logLevel"`
	MaxDepth            int      `json:"maxDepth"`
	MaxWorkers          int      `json:"maxWorkers"`
	MaxDomains          int      `json:"maxDomains"`
	SeedURLs            []string `json:"seedURLs"`
	AllowedTLDs         []string `json:"allowedTLDs"`
	BlockedSubdomains   []string `json:"blockedSubdomains"`
	BlockedPathSegments []string `json:"blockedPathSegments"`
	BlockedHostnames    []string `json:"blockedHostnames"`
	PassSubdomains      []string `json:"passSubdomains"`
	PassPathSegments    []string `json:"passPathSegments"`
}

type Loader interface {
	Load(ctx context.Context) (*Config, error)
}
