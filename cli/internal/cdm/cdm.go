package cdm

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const DefaultCDMURL = "https://config.blocks.ai/config.json"

const cacheTTL = 7 * 24 * time.Hour

type Keyset struct {
	PublishKey   string `json:"publishKey"`
	SubscribeKey string `json:"subscribeKey"`
}

type ApiConfig struct {
	BaseURL  string `json:"baseUrl"`
	ClientID string `json:"clientId"`
}

type Config struct {
	Playground Keyset    `json:"playground"`
	Network    Keyset    `json:"network"`
	Api        ApiConfig `json:"api"`
}

var (
	cached    *Config
	cachedErr error
	once      sync.Once
)

// Get returns the CDM config, fetching it on first call and caching the result.
func Get() (*Config, error) {
	once.Do(func() {
		cached, cachedErr = fetch()
	})
	return cached, cachedErr
}

// Reset clears the cached config (for testing).
func Reset() {
	once = sync.Once{}
	cached = nil
	cachedErr = nil
}

func localConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".blocks", "config.json")
}

func loadLocal() (*Config, error) {
	path := localConfigPath()
	if path == "" {
		return nil, fmt.Errorf("cannot determine home directory")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if time.Since(info.ModTime()) > cacheTTL {
		return nil, fmt.Errorf("local config expired")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Api.BaseURL == "" {
		return nil, fmt.Errorf("local config missing api.baseUrl")
	}
	return &cfg, nil
}

func saveLocal(cfg *Config) error {
	path := localConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func fetch() (*Config, error) {
	url := os.Getenv("BLOCKS_CDM_URL")
	if url == "" {
		if cfg, err := loadLocal(); err == nil {
			return cfg, nil
		}
		url = DefaultCDMURL
	}

	// Disable HTTP/2 — Go's h2 implementation hangs on some Windows TLS stacks.
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:        http.ProxyFromEnvironment,
			TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		// Single retry after 1s
		time.Sleep(1 * time.Second)
		resp, err = client.Get(url)
	}
	if err != nil {
		return nil, fmt.Errorf("CDM config fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CDM config fetch failed: HTTP %d", resp.StatusCode)
	}

	var cfg Config
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("CDM config parse failed: %w", err)
	}

	if cfg.Api.BaseURL == "" {
		return nil, fmt.Errorf("CDM config missing api.baseUrl")
	}

	if url == DefaultCDMURL {
		if err := saveLocal(&cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to cache CDM config locally: %v\n", err)
		}
	}

	return &cfg, nil
}
