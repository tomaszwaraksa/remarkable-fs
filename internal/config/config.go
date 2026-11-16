package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"remarkable-fs/internal/converter"
	"remarkable-fs/internal/paths"
)

//go:embed default_configuration.yaml
var DefaultConfigYAML []byte

// PDFMargins defines margin settings in millimeters for PDF conversion.
type PDFMargins struct {
	Top    float64 `yaml:"top,omitempty"`
	Bottom float64 `yaml:"bottom,omitempty"`
	Left   float64 `yaml:"left,omitempty"`
	Right  float64 `yaml:"right,omitempty"`
}

// PDFConfig represents user-configurable options for PDF conversion.
type PDFConfig struct {
	PageSize       string      `yaml:"page_size,omitempty"`
	Orientation    string      `yaml:"orientation,omitempty"`
	PageWidthMM    float64     `yaml:"page_width_mm,omitempty"`
	PageHeightMM   float64     `yaml:"page_height_mm,omitempty"`
	Margins        *PDFMargins `yaml:"margins,omitempty"`
	MarginTopMM    float64     `yaml:"margin_top_mm,omitempty"`
	MarginBottomMM float64     `yaml:"margin_bottom_mm,omitempty"`
	MarginLeftMM   float64     `yaml:"margin_left_mm,omitempty"`
	MarginRightMM  float64     `yaml:"margin_right_mm,omitempty"`
	FontSize       float64     `yaml:"font_size,omitempty"`
	LineHeight     float64     `yaml:"line_height,omitempty"`
	CodeFontSize   float64     `yaml:"code_font_size,omitempty"`
}

// Config represents the application configuration stored in ~/.config/remarkable-fs/configuration.yaml.
type Config struct {
	Host             string     `yaml:"host"`
	FallbackHosts    []string   `yaml:"fallback_hosts,omitempty"`
	Port             int        `yaml:"port"`
	User             string     `yaml:"user"`
	SSHKey           string     `yaml:"ssh_key,omitempty"`
	Password         string     `yaml:"password,omitempty"`
	MountPoint       string     `yaml:"mount_point,omitempty"`
	LogFile          string     `yaml:"log_file,omitempty"`
	MaxUploadSizeMB  int        `yaml:"max_upload_size_mb,omitempty"`
	TextUploadFormat string     `yaml:"text_upload_format,omitempty"`
	Converter        string     `yaml:"converter,omitempty"`
	EnableWiFiSSH    *bool      `yaml:"enable_wifi_ssh,omitempty"`
	PDF              *PDFConfig `yaml:"pdf,omitempty"`
}

const (
	DefaultConfigDir        = "~/.config/remarkable-fs"
	DefaultConfigFile       = "configuration.yaml"
	DefaultPort             = 22
	DefaultUser             = "root"
	DefaultLogFile          = "~/.config/remarkable-fs/log.txt"
	DefaultMaxUploadSizeMB  = 200
	DefaultTextUploadFormat = "epub"
)

// IsWiFiSSHEnabled reports whether remarkable-fs should switch on SSH over Wi-Fi on the tablet (default: yes).
func (c *Config) IsWiFiSSHEnabled() bool {
	return c.EnableWiFiSSH == nil || *c.EnableWiFiSSH
}

// ResolvedTextConverter returns the configured converter format ("epub" or "pdf"), defaulting to "epub".
func (c *Config) ResolvedTextConverter() string {
	val := strings.ToLower(strings.TrimSpace(c.Converter))
	if val == "" {
		val = strings.ToLower(strings.TrimSpace(c.TextUploadFormat))
	}
	if val == "pdf" {
		return "pdf"
	}
	return "epub"
}

// ResolvedPDFOptions converts the Config.PDF configuration into converter.PDFOptions.
func (c *Config) ResolvedPDFOptions() converter.PDFOptions {
	opts := converter.DefaultPDFOptions()
	if c.PDF == nil {
		return opts
	}

	if c.PDF.PageSize != "" {
		opts.PageSize = c.PDF.PageSize
	}
	if c.PDF.Orientation != "" {
		opts.Orientation = c.PDF.Orientation
	}
	if c.PDF.PageWidthMM > 0 {
		opts.PageWidthMM = c.PDF.PageWidthMM
	}
	if c.PDF.PageHeightMM > 0 {
		opts.PageHeightMM = c.PDF.PageHeightMM
	}

	// Nested margins
	if c.PDF.Margins != nil {
		if c.PDF.Margins.Top > 0 {
			opts.MarginTopMM = c.PDF.Margins.Top
		}
		if c.PDF.Margins.Bottom > 0 {
			opts.MarginBottomMM = c.PDF.Margins.Bottom
		}
		if c.PDF.Margins.Left > 0 {
			opts.MarginLeftMM = c.PDF.Margins.Left
		}
		if c.PDF.Margins.Right > 0 {
			opts.MarginRightMM = c.PDF.Margins.Right
		}
	}
	// Flat margin overrides
	if c.PDF.MarginTopMM > 0 {
		opts.MarginTopMM = c.PDF.MarginTopMM
	}
	if c.PDF.MarginBottomMM > 0 {
		opts.MarginBottomMM = c.PDF.MarginBottomMM
	}
	if c.PDF.MarginLeftMM > 0 {
		opts.MarginLeftMM = c.PDF.MarginLeftMM
	}
	if c.PDF.MarginRightMM > 0 {
		opts.MarginRightMM = c.PDF.MarginRightMM
	}

	if c.PDF.FontSize > 0 {
		opts.FontSize = c.PDF.FontSize
	}
	if c.PDF.LineHeight > 0 {
		opts.LineHeight = c.PDF.LineHeight
	}
	if c.PDF.CodeFontSize > 0 {
		opts.CodeFontSize = c.PDF.CodeFontSize
	}

	return converter.ResolvePDFOptions(opts)
}

// DefaultConfig returns a new configuration parsed from the embedded default template,
// so the template is the single source of the default values.
func DefaultConfig() *Config {
	cfg := &Config{}
	if err := yaml.Unmarshal(DefaultConfigYAML, cfg); err != nil {
		// The template is compiled in and covered by tests; a parse error is a build mistake.
		panic(fmt.Sprintf("embedded default_configuration.yaml is invalid: %v", err))
	}
	return cfg
}

// MaxUploadSizeBytes returns the maximum upload size in bytes, or 0 if unlimited.
func (c *Config) MaxUploadSizeBytes() int64 {
	if c.MaxUploadSizeMB < 0 {
		return 0 // unlimited
	}
	if c.MaxUploadSizeMB == 0 {
		return int64(DefaultMaxUploadSizeMB) * 1024 * 1024
	}
	return int64(c.MaxUploadSizeMB) * 1024 * 1024
}

// GetDefaultConfigPath returns the absolute path to ~/.config/remarkable-fs/configuration.yaml.
func GetDefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "remarkable-fs", DefaultConfigFile), nil
}

// LoadConfig loads the YAML configuration from filePath.
// If filePath is empty, it uses the default location.
// If the file does not exist, it writes the embedded default configuration template and returns it.
func LoadConfig(filePath string) (*Config, string, error) {
	var targetPath string
	if filePath != "" {
		targetPath = paths.ExpandHome(filePath)
	} else {
		var err error
		targetPath, err = GetDefaultConfigPath()
		if err != nil {
			return nil, "", err
		}
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Write the embedded template as-is, so its comments explain each setting.
			cfg := DefaultConfig()
			if err := writeDefaultTemplate(targetPath); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not write default configuration: %v\n", err)
			}
			return cfg, targetPath, nil
		}
		return nil, targetPath, fmt.Errorf("failed to read config file %s: %w", targetPath, err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, targetPath, fmt.Errorf("failed to parse config YAML at %s: %w", targetPath, err)
	}

	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.User == "" {
		cfg.User = DefaultUser
	}
	if cfg.LogFile == "" {
		cfg.LogFile = DefaultLogFile
	}
	if len(cfg.FallbackHosts) == 0 {
		cfg.FallbackHosts = DefaultConfig().FallbackHosts
	}

	return cfg, targetPath, nil
}

// hostLine matches the top-level "host:" setting, so it can be replaced on its own.
var hostLine = regexp.MustCompile(`(?m)^host:.*$`)

// SaveHost stores host as the preferred tablet host. An existing file only gets its "host:" line
// replaced (or one appended), so comments and all other settings stay as they are. A missing file
// is first created from the commented template. The file is replaced atomically, keeping its mode.
func SaveHost(targetPath string, host string) error {
	expandedPath := paths.ExpandHome(targetPath)
	if _, err := os.Stat(expandedPath); os.IsNotExist(err) {
		if err := writeDefaultTemplate(expandedPath); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(expandedPath)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", expandedPath, err)
	}
	info, err := os.Stat(expandedPath)
	if err != nil {
		return fmt.Errorf("failed to read permissions of %s: %w", expandedPath, err)
	}
	return paths.WriteFileAtomic(expandedPath, withHost(data, host), info.Mode().Perm())
}

// withHost returns data with its top-level "host:" line set to host, appending one if missing.
func withHost(data []byte, host string) []byte {
	line := []byte("host: " + strconv.Quote(host))
	if hostLine.Match(data) {
		return hostLine.ReplaceAllLiteral(data, line)
	}
	return append(append(bytes.TrimRight(data, "\n"), '\n'), append(line, '\n')...)
}

// writeDefaultTemplate writes the embedded, commented default configuration to targetPath.
func writeDefaultTemplate(targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return fmt.Errorf("failed to create config directory for %s: %w", targetPath, err)
	}
	if err := os.WriteFile(targetPath, DefaultConfigYAML, 0600); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", targetPath, err)
	}
	return nil
}

// SaveConfig writes the given configuration to targetPath in YAML format.
func SaveConfig(targetPath string, cfg *Config) error {
	expandedPath := paths.ExpandHome(targetPath)
	dir := filepath.Dir(expandedPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to encode configuration to YAML: %w", err)
	}

	if err := os.WriteFile(expandedPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", expandedPath, err)
	}

	return nil
}
