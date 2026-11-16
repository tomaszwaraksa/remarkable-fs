package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSaveAndLoad(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "remarkable-test-cfg-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfgPath := filepath.Join(tempDir, "sub", "configuration.yaml")

	cfg := &Config{
		Host:          "remarkable.local",
		FallbackHosts: []string{"remarkable.local", "10.11.99.1"},
		Port:          22,
		User:          "root",
		MountPoint:    "/mnt/remarkable",
		LogFile:       "~/.config/remarkable-fs/log.txt",
	}

	if err := SaveConfig(cfgPath, cfg); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	loaded, loadedPath, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if loadedPath != cfgPath {
		t.Errorf("Expected path %s, got %s", cfgPath, loadedPath)
	}
	if loaded.Host != "remarkable.local" {
		t.Errorf("Expected host remarkable.local, got %s", loaded.Host)
	}
	if len(loaded.FallbackHosts) != 2 || loaded.FallbackHosts[0] != "remarkable.local" {
		t.Errorf("Expected fallback_hosts [remarkable.local, 10.11.99.1], got %v", loaded.FallbackHosts)
	}
	if loaded.Port != 22 {
		t.Errorf("Expected port 22, got %d", loaded.Port)
	}
	if loaded.User != "root" {
		t.Errorf("Expected user root, got %s", loaded.User)
	}
	if loaded.MountPoint != "/mnt/remarkable" {
		t.Errorf("Expected mount_point /mnt/remarkable, got %s", loaded.MountPoint)
	}
	if loaded.LogFile != "~/.config/remarkable-fs/log.txt" {
		t.Errorf("Expected log_file ~/.config/remarkable-fs/log.txt, got %s", loaded.LogFile)
	}
}

func TestLoadNonExistentConfigReturnsDefault(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "remarkable-test-cfg-default-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	targetPath := filepath.Join(tempDir, "configuration.yaml")
	loaded, _, err := LoadConfig(targetPath)
	if err != nil {
		t.Fatalf("Expected no error for nonexistent file, got %v", err)
	}
	if loaded.User != DefaultUser {
		t.Errorf("Expected default user %s, got %s", DefaultUser, loaded.User)
	}
	if loaded.Port != DefaultPort {
		t.Errorf("Expected default port %d, got %d", DefaultPort, loaded.Port)
	}
	if len(loaded.FallbackHosts) != 2 {
		t.Errorf("Expected 2 fallback hosts in default config, got %d", len(loaded.FallbackHosts))
	}
	if loaded.ResolvedTextConverter() != "epub" {
		t.Errorf("Expected default converter to be epub, got %s", loaded.ResolvedTextConverter())
	}
}

func TestResolvedTextConverter(t *testing.T) {
	c1 := &Config{}
	if c1.ResolvedTextConverter() != "epub" {
		t.Errorf("Expected empty config to default to epub, got %s", c1.ResolvedTextConverter())
	}

	c2 := &Config{Converter: "pdf"}
	if c2.ResolvedTextConverter() != "pdf" {
		t.Errorf("Expected converter 'pdf' to resolve to pdf, got %s", c2.ResolvedTextConverter())
	}

	c3 := &Config{TextUploadFormat: "PDF"}
	if c3.ResolvedTextConverter() != "pdf" {
		t.Errorf("Expected text_upload_format 'PDF' to resolve to pdf, got %s", c3.ResolvedTextConverter())
	}

	c4 := &Config{Converter: "EPUB", TextUploadFormat: "pdf"}
	if c4.ResolvedTextConverter() != "epub" {
		t.Errorf("Expected Converter to take precedence over TextUploadFormat, got %s", c4.ResolvedTextConverter())
	}
}

func TestResolvedPDFOptions(t *testing.T) {
	// Empty config defaults to remarkable portrait
	c1 := &Config{}
	opts1 := c1.ResolvedPDFOptions()
	if opts1.PageSize != "remarkable" || opts1.Orientation != "portrait" {
		t.Errorf("Expected default remarkable portrait, got %+v", opts1)
	}

	// YAML config with A4 landscape and custom margins
	c2 := &Config{
		PDF: &PDFConfig{
			PageSize:    "a4",
			Orientation: "landscape",
			Margins: &PDFMargins{
				Top:    20.0,
				Bottom: 20.0,
				Left:   25.0,
				Right:  25.0,
			},
			FontSize:   11.0,
			LineHeight: 5.8,
		},
	}
	opts2 := c2.ResolvedPDFOptions()
	if opts2.PageSize != "a4" || opts2.Orientation != "landscape" {
		t.Errorf("Expected A4 landscape, got %+v", opts2)
	}
	if opts2.PageWidthMM != 297.0 || opts2.PageHeightMM != 210.0 {
		t.Errorf("Expected A4 dimensions 297x210 mm, got %.2fx%.2f", opts2.PageWidthMM, opts2.PageHeightMM)
	}
	if opts2.MarginTopMM != 20.0 || opts2.MarginLeftMM != 25.0 {
		t.Errorf("Expected custom margins (20, 25), got Top=%.1f, Left=%.1f", opts2.MarginTopMM, opts2.MarginLeftMM)
	}
	if opts2.FontSize != 11.0 || opts2.LineHeight != 5.8 {
		t.Errorf("Expected custom typography (11pt, 5.8mm), got %+v", opts2)
	}
}

func TestLoadConfigWritesCommentedTemplate(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "new", "configuration.yaml")

	cfg, _, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	written, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("Expected a config file to be written: %v", err)
	}
	if string(written) != string(DefaultConfigYAML) {
		t.Errorf("Expected the embedded template to be written as-is, including comments")
	}
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("Stat %s failed: %v", cfgPath, err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Expected config file mode 0600, got %v", info.Mode().Perm())
	}
	if cfg.Host != "remarkable.local" || !cfg.IsWiFiSSHEnabled() {
		t.Errorf("Expected template defaults, got host %q, Wi-Fi SSH %v", cfg.Host, cfg.IsWiFiSSHEnabled())
	}
}

func TestIsWiFiSSHEnabled(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"missing key defaults to on", "host: remarkable.local\n", true},
		{"explicitly on", "enable_wifi_ssh: true\n", true},
		{"explicitly off", "enable_wifi_ssh: false\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".yaml")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig failed: %v", err)
			}
			if got := cfg.IsWiFiSSHEnabled(); got != tc.want {
				t.Errorf("IsWiFiSSHEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
	if !(&Config{}).IsWiFiSSHEnabled() {
		t.Errorf("An empty Config should default to Wi-Fi SSH on")
	}
}

func TestSaveHostKeepsCommentsAndSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "configuration.yaml")
	original := "# Target tablet host\nhost: \"remarkable.local\"\n\n# Mount point\nmount_point: \"~/tablet\"\nconverter: pdf   # my choice\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil { // explicit, so the umask doesn't matter
		t.Fatal(err)
	}

	if err := SaveHost(path, "192.168.1.50"); err != nil {
		t.Fatalf("SaveHost failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	want := "# Target tablet host\nhost: \"192.168.1.50\"\n\n# Mount point\nmount_point: \"~/tablet\"\nconverter: pdf   # my choice\n"
	if string(got) != want {
		t.Errorf("Expected only the host line to change.\ngot:\n%s\nwant:\n%s", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s failed: %v", path, err)
	}
	if info.Mode().Perm() != 0640 {
		t.Errorf("Expected permissions to be kept (0640), got %v", info.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".configuration.yaml.*")); len(leftovers) > 0 {
		t.Errorf("Expected no temporary files left behind, found %v", leftovers)
	}
	cfg, _, err := LoadConfig(path)
	if err != nil || cfg.Host != "192.168.1.50" || cfg.MountPoint != "~/tablet" || cfg.ResolvedTextConverter() != "pdf" {
		t.Errorf("Unexpected config after SaveHost: %+v (err %v)", cfg, err)
	}
}

func TestSaveHostAppendsOrCreates(t *testing.T) {
	dir := t.TempDir()

	noHost := filepath.Join(dir, "no-host.yaml")
	if err := os.WriteFile(noHost, []byte("port: 22\nuser: root"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveHost(noHost, "fe80::1"); err != nil {
		t.Fatalf("SaveHost failed: %v", err)
	}
	if got, _ := os.ReadFile(noHost); string(got) != "port: 22\nuser: root\nhost: \"fe80::1\"\n" {
		t.Errorf("Expected host appended on its own line, got:\n%s", got)
	}

	missing := filepath.Join(dir, "sub", "missing.yaml")
	if err := SaveHost(missing, "remarkable.local"); err != nil {
		t.Fatalf("SaveHost on a missing file failed: %v", err)
	}
	written, err := os.ReadFile(missing)
	if err != nil {
		t.Fatalf("Expected the missing file to be created: %v", err)
	}
	if !strings.Contains(string(written), "# Fallback candidate hosts") {
		t.Errorf("Expected a new file to keep the template's comments, got:\n%s", written)
	}
	cfg, _, err := LoadConfig(missing)
	if err != nil || cfg.Host != "remarkable.local" {
		t.Errorf("Expected a new config with the host, got %+v (err %v)", cfg, err)
	}
}

func TestDefaultConfigComesFromTemplate(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Host != "remarkable.local" || cfg.Port != 22 || cfg.User != "root" || cfg.MountPoint != "~/remarkable" {
		t.Errorf("Unexpected defaults from the template: %+v", cfg)
	}
	if len(cfg.FallbackHosts) != 2 || cfg.FallbackHosts[1] != "10.11.99.1" {
		t.Errorf("Expected the template's fallback hosts, got %v", cfg.FallbackHosts)
	}
	cfg.FallbackHosts[0] = "changed"
	if DefaultConfig().FallbackHosts[0] == "changed" {
		t.Errorf("DefaultConfig must return an independent copy")
	}
}
