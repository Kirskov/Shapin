package scanner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const defaultConfigFile = ".digestify.json"

// ConfigFile is the structure of a .digestify.json config file.
// All fields are optional; CLI flags take precedence over file values.
type ConfigFile struct {
	Path         *string  `json:"path"`
	DryRun       *bool    `json:"dry-run"`
	GitHubToken  *string  `json:"github-token"`
	GitLabToken  *string  `json:"gitlab-token"`
	GitLabHost   *string  `json:"gitlab-host"`
	ForgejoHost  *string  `json:"forgejo-host"`
	ForgejoToken *string  `json:"forgejo-token"`
	PinActions   *bool    `json:"pin-actions"`
	PinImages    *bool    `json:"pin-images"`
	Exclude      []string `json:"exclude"`
}

// LoadConfigFile reads a .digestify.json file from the given path.
// If path is empty it looks for .digestify.json in the current directory.
func LoadConfigFile(path string) (*ConfigFile, error) {
	if path == "" {
		path = defaultConfigFile
	}

	data, err := os.ReadFile(path) // #nosec G304 — path is user-supplied --config flag, intentional
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // no config file is fine
	}
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg ConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return &cfg, nil
}

// ApplyTo merges config file values into cfg, only for fields not already set
// by the caller (i.e. still holding their zero/default value indicated by set).
// The set map lists which flag names were explicitly provided on the CLI.
func (f *ConfigFile) ApplyTo(cfg *Config, set map[string]bool) {
	if f == nil {
		return
	}
	applyString(f.Path, &cfg.Path, set["path"])
	applyBool(f.DryRun, &cfg.DryRun, set["dry-run"])
	applyString(f.GitHubToken, &cfg.GitHubToken, set["github-token"])
	applyString(f.GitLabToken, &cfg.GitLabToken, set["gitlab-token"])
	applyString(f.GitLabHost, &cfg.GitLabHost, set["gitlab-host"])
	applyString(f.ForgejoHost, &cfg.ForgejoHost, set["forgejo-host"])
	applyString(f.ForgejoToken, &cfg.ForgejoToken, set["forgejo-token"])
	applyBool(f.PinActions, &cfg.PinActions, set["pin-actions"])
	applyBool(f.PinImages, &cfg.PinImages, set["pin-images"])
	if len(f.Exclude) > 0 && !set["exclude"] {
		cfg.Exclude = f.Exclude
	}
}

func applyString(src *string, dst *string, explicit bool) {
	if src != nil && !explicit {
		*dst = *src
	}
}

func applyBool(src *bool, dst *bool, explicit bool) {
	if src != nil && !explicit {
		*dst = *src
	}
}

