// Package config defines the Config struct and a config Loader interface. Implementations live in sub packages
package config

import "context"

type Config struct {
	MaxDepth            int      `json:"maxDepth"`
	MaxDomains          int      `json:"maxDomains"`
	SeedURLs            []string `json:"seedURLs"`
	BlockedSubdomains   []string `json:"blockedSubdomains"`
	BlockedPathSegments []string `json:"blockedPathSegments"`
	BlockedHostnames    []string `json:"blockedHostnames"`
	AllowedSubdomains   []string `json:"allowedSubdomains"`
	AllowedPathSegments []string `json:"allowedPathSegments"`
}

type Loader interface {
	Load(ctx context.Context) (*Config, error)
}
