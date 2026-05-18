package config

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// Format identifies the on-disk format of a config file.
type Format int

const (
	FormatYAML Format = iota
	FormatTOML
)

// LoadFile reads and decodes the config at path, applies defaults, and
// validates. Format is detected by file extension first
// (.yaml/.yml/.json → YAML, .toml → TOML); if the extension is
// unrecognized, the contents are sniffed (see detectFormat).
func LoadFile(path string) (*Config, error) {
	cfg, err := LoadFileRaw(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.Finalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadFileRaw reads and decodes the config at path without applying
// defaults or validating. Callers that need to mutate the result before
// use (for instance, applying CLI overrides) should call Finalize after
// their mutations. Format detection matches LoadFile.
func LoadFileRaw(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	format, ok := formatFromExt(path)
	if !ok {
		format = detectFormat(data)
	}
	return decode(data, format)
}

// Load reads from r, auto-detecting YAML vs TOML by sniffing the bytes,
// and applies defaults plus validation. Use LoadFile when you have a
// path so the extension hint applies first.
func Load(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := decode(data, detectFormat(data))
	if err != nil {
		return nil, err
	}
	if err := cfg.Finalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Finalize applies defaults and validates the config. Call this after
// decoding (via LoadFileRaw) and any post-decode mutations such as CLI
// overrides. LoadFile and Load call Finalize automatically.
func (c *Config) Finalize() error {
	if err := c.applyDefaults(); err != nil {
		return err
	}
	return c.Validate()
}

func decode(data []byte, format Format) (*Config, error) {
	var cfg Config
	switch format {
	case FormatTOML:
		dec := toml.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("decode toml config: %w", err)
		}
	default:
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("decode yaml config: %w", err)
		}
	}
	return &cfg, nil
}

func formatFromExt(path string) (Format, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml", ".json":
		// JSON is parsed by the YAML decoder since YAML 1.2 is a JSON superset.
		return FormatYAML, true
	case ".toml":
		return FormatTOML, true
	}
	return 0, false
}

// detectFormat sniffs raw bytes. Any non-blank, non-comment line whose
// first character is '[' is treated as a TOML table header ([name] or
// [[name]]); otherwise the content is treated as YAML.
func detectFormat(data []byte) Format {
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			return FormatTOML
		}
	}
	return FormatYAML
}
