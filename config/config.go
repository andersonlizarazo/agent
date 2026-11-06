package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"charm.land/fantasy/providers/openai"
	"github.com/BurntSushi/toml"
)

const (
	EnvBase           = "AGENT_"
	EnvAgentConfigDir = EnvBase + "CONFIG_DIR"
)

const (
	DefaultConfigSubdir = "agent"
	DefaultFile         = "config.toml"
	DefaultPrivateFile  = "private.toml"
)

type Config struct {
	OpenAI struct {
		ReasoningEffort openai.ReasoningEffort `toml:"reasoning_effort"`
	} `toml:"openai"`
}

type ConfigPrivate struct {
	OpenAI struct {
		BaseURL       string `toml:"base_url"`
		APIKey        string `toml:"api_key"`
		LanguageModel string `toml:"language_model"`
	} `toml:"openai"`
}

func LoadConfig(configData, privateConfigData []byte) (Config, ConfigPrivate, error) {
	var config Config
	if err := toml.Unmarshal(configData, &config); err != nil {
		return Config{}, ConfigPrivate{}, err
	}

	if !slices.Contains([]openai.ReasoningEffort{
		openai.ReasoningEffortNone,
		openai.ReasoningEffortMinimal,
		openai.ReasoningEffortLow,
		openai.ReasoningEffortMedium,
		openai.ReasoningEffortHigh,
		openai.ReasoningEffortXHigh,
		openai.ReasoningEffortMax,
	}, config.OpenAI.ReasoningEffort) {
		return Config{}, ConfigPrivate{}, fmt.Errorf("invalid reasoning effort: %q", config.OpenAI.ReasoningEffort)
	}

	var configPrivate ConfigPrivate
	if err := toml.Unmarshal(privateConfigData, &configPrivate); err != nil {
		return Config{}, ConfigPrivate{}, err
	}

	if configPrivate.OpenAI.BaseURL == "" {
		return Config{}, ConfigPrivate{}, fmt.Errorf("no base url was specified")
	}

	if configPrivate.OpenAI.LanguageModel == "" {
		return Config{}, ConfigPrivate{}, fmt.Errorf("no language model was specified")
	}

	return config, configPrivate, nil
}

func Directory(getenv func(string) string) (string, error) {
	if configDir := getenv(EnvAgentConfigDir); configDir != "" {
		return configDir, nil
	}

	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(userConfigDir, DefaultConfigSubdir), nil
}
