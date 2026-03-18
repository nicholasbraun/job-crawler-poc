// Package jsonloader implements the config.Loader interface.
// This will read a config.json file
package jsonloader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nicholasbraun/job-crawler-poc/internal/config"
)

var _ config.Loader = &JSONLoader{}

type JSONLoader struct {
	path string
}

func (l *JSONLoader) Load(ctx context.Context) (*config.Config, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, fmt.Errorf("config: error reading json config file %w", err)
	}

	var config config.Config
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("config: error unmarshalling data %w", err)
	}

	return &config, nil
}

func NewJSONLoader(path string) *JSONLoader {
	return &JSONLoader{
		path: path,
	}
}
