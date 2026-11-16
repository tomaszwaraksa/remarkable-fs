package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"remarkable-fs/internal/config"
	"remarkable-fs/internal/converter"
	"remarkable-fs/internal/discovery"
	"remarkable-fs/internal/fusefs"
	"remarkable-fs/internal/instance"
	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/paths"
	"remarkable-fs/internal/remarkable"
	"remarkable-fs/internal/sshclient"
)

// version is stamped in by build.sh (-ldflags "-X main.version=..."): the release tag, or the
// git commit for a developer build. "dev" for a plain go build.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Parse command-line flags
	mountFlag := flag.String("mount", "", "Directory to mount the reMarkable filesystem (default: mount_point from the settings)")
	hostFlag := flag.String("host", "", "reMarkable hostname or IP address (e.g. remarkable.local or 192.168.x.x)")
	userFlag := flag.String("user", "", "SSH username (default: root)")
	portFlag := flag.Int("port", 0, "SSH port (default: 22)")
	keyFlag := flag.String("key", "", "Path to SSH private key file")
	passwordFlag := flag.String("password", "", "SSH password")
	configFlag := flag.String("config", "", "Custom configuration file path (default: ~/.config/remarkable-fs/configuration.yaml)")
	logFlag := flag.String("log", "", "Log file path (default: ~/.config/remarkable-fs/log.txt)")
	maxUploadSizeMBFlag := flag.Int("max-upload-size-mb", 0, "Maximum upload size limit in MB (0 = use config, default 200MB, <0 means unlimited)")
	converterFlag := flag.String("converter", "", "Target converter for .txt and .md uploads ('epub' or 'pdf', default: 'epub')")
	textFormatFlag := flag.String("text-format", "", "Alias for --converter ('epub' or 'pdf')")
	pdfPageSizeFlag := flag.String("pdf-page-size", "", "PDF page size preset ('remarkable', 'a4', 'a5', 'letter', 'legal', or 'custom')")
	pdfOrientationFlag := flag.String("pdf-orientation", "", "PDF page orientation ('portrait' or 'landscape')")
	versionFlag := flag.Bool("version", false, "Print the version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Println("remarkable-fs " + version)
		return nil
	}

	// 2. Load configuration from ~/.config/remarkable-fs/configuration.yaml
	cfg, configPath, err := config.LoadConfig(*configFlag)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize PDF rendering options from config and flags
	pdfOpts := cfg.ResolvedPDFOptions()
	if *pdfPageSizeFlag != "" {
		pdfOpts.PageSize = *pdfPageSizeFlag
	}
	if *pdfOrientationFlag != "" {
		pdfOpts.Orientation = *pdfOrientationFlag
	}
	converter.SetPDFOptions(pdfOpts)

	// 3. Resolve mount point
	mountPoint := *mountFlag
	if mountPoint == "" {
		mountPoint = cfg.MountPoint
	}
	if mountPoint == "" {
		flag.Usage()
		return fmt.Errorf("mount point is required; please specify --mount <folder>")
	}
	mountPoint = paths.ExpandHome(mountPoint)

	// 4. One copy per computer: a second one would restart the tablet's screen and write
	// the shared log on its own. Checked before the log is opened, so a refused copy
	// leaves it alone.
	lock, err := instance.Acquire(instance.DefaultLockPath(), mountPoint)
	if err != nil {
		return err
	}
	defer lock.Release()

	// 5. Initialize logger
	logPath := cfg.LogFile
	if *logFlag != "" {
		logPath = *logFlag
	}
	logInstance, err := logger.Init(logPath, true)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	defer logInstance.Close()

	logger.Info("Starting remarkable-fs... (version %s)", version)

	// 6. Resolve reMarkable host
	targetHost := *hostFlag
	shouldSaveHost := false

	if targetHost == "" {
		// Check saved configuration
		if cfg.Host != "" {
			targetHost = cfg.Host
			logger.Info("Using preferred reMarkable host from config: %s", targetHost)
		} else {
			// Auto-detect reMarkable on local network
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			candidates := discovery.Discover(ctx, 2*time.Second)
			cancel()

			chosenHost, err := discovery.PromptUserSelection(candidates)
			if err != nil {
				return fmt.Errorf("host selection failed: %w", err)
			}
			targetHost = chosenHost
			shouldSaveHost = true
		}
	}

	// If host was newly selected or auto-detected, save as preferred host in configuration.yaml
	if shouldSaveHost {
		cfg.Host = targetHost
		if err := config.SaveHost(configPath, targetHost); err != nil {
			logger.Error("Warning: failed to save preferred host to %s: %v", configPath, err)
		} else {
			logger.Info("Saved preferred host %s to %s", targetHost, configPath)
			fmt.Printf("[remarkable-fs] Saved %s as preferred host in %s\n", targetHost, configPath)
		}
	}

	// 7. Resolve SSH parameters (CLI flags override config)
	sshUser := cfg.User
	if *userFlag != "" {
		sshUser = *userFlag
	}
	sshPort := cfg.Port
	if *portFlag != 0 {
		sshPort = *portFlag
	}
	sshKey := cfg.SSHKey
	if *keyFlag != "" {
		sshKey = *keyFlag
	}
	sshPassword := cfg.Password
	if *passwordFlag != "" {
		sshPassword = *passwordFlag
	}

	// 8. Mount right away, even while the tablet sleeps: the folder stays read-only and empty
	// until the first connection, so nothing saved there can land on the local disk.
	client := sshclient.New(sshclient.Config{
		Host:           targetHost,
		FallbackHosts:  cfg.FallbackHosts,
		Port:           sshPort,
		User:           sshUser,
		KeyPath:        sshKey,
		Password:       sshPassword,
		Timeout:        10 * time.Second,
		KnownHostsPath: filepath.Join(filepath.Dir(configPath), "known_hosts"),
	})
	defer client.Close()
	repo := remarkable.NewRepository(client)

	maxUploadSize := cfg.MaxUploadSizeBytes()
	if *maxUploadSizeMBFlag != 0 {
		if *maxUploadSizeMBFlag < 0 {
			maxUploadSize = 0 // unlimited
		} else {
			maxUploadSize = int64(*maxUploadSizeMBFlag) * 1024 * 1024
		}
	}

	textUploadFormat := cfg.ResolvedTextConverter()
	if *converterFlag != "" {
		textUploadFormat = *converterFlag
	} else if *textFormatFlag != "" {
		textUploadFormat = *textFormatFlag
	}

	mounted, err := fusefs.Mount(mountPoint, repo.Root(), client, repo, maxUploadSize, textUploadFormat)
	if err != nil {
		logger.Error("Could not mount the folder: %v", err)
		return fmt.Errorf("failed to mount FUSE: %w", err)
	}

	fmt.Printf("\n✓ Mounted reMarkable filesystem at: %s (documents appear once the tablet is connected)\n", mountPoint)
	fmt.Printf("✓ Activity logged to: %s\n", logPath)
	fmt.Printf("Press Ctrl+C to unmount and exit.\n\n")

	// 9. Connect and load the documents in the background, for as long as it takes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loaderDone := make(chan struct{})
	go func() {
		defer close(loaderDone)
		connectAndLoad(ctx, client, repo, mounted, cfg)
	}()

	// 10. Run until a shutdown signal, or until the folder is unmounted from outside
	waitForShutdown(mounted, mountPoint)
	cancel()
	select {
	case <-loaderDone:
	case <-time.After(2 * time.Second): // a tablet command in progress may take a while; don't hang the stop
	}
	logger.Info("remarkable-fs exited cleanly")
	return nil
}

// connectAndLoad waits for the tablet, prepares it, reads its documents and shows them in the
// mounted folder. It retries until it succeeds or ctx is cancelled.
func connectAndLoad(ctx context.Context, client *sshclient.Client, repo *remarkable.Repository, mounted *fusefs.Mounted, cfg *config.Config) {
	if err := client.WaitForConnection(ctx); err != nil {
		return
	}

	// Switch on SSH over Wi-Fi (unless disabled), install the public key, and keep tablet sleep uninhibited
	if cfg.IsWiFiSSHEnabled() {
		client.EnableWiFiSSH()
	} else {
		logger.Info("Leaving SSH over Wi-Fi on the tablet as it is (enable_wifi_ssh: false)")
	}
	client.InstallPublicKey()
	client.EnsureSleepUninhibited()

	for wait := 2 * time.Second; ; wait = min(wait*2, 30*time.Second) {
		_, err := repo.LoadTree()
		if err == nil {
			mounted.AttachTree()
			return
		}
		logger.Error("Could not read the documents from the tablet: %v (retrying in %v)", err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// waitForShutdown blocks until SIGINT/SIGTERM or until the folder gets unmounted from outside.
// On a signal it unmounts; a busy folder is detached lazily, so stopping never hangs.
func waitForShutdown(mounted *fusefs.Mounted, mountPoint string) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	serverDone := make(chan struct{})
	go func() {
		mounted.Wait()
		close(serverDone)
	}()

	select {
	case <-serverDone:
	case sig := <-sigChan:
		logger.Info("Received signal %s, initiating graceful unmount...", sig)
		if err := fusefs.Unmount(mounted, mountPoint); err != nil {
			return
		}
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second): // a lazily detached folder stays served until its files close
		}
	}
}
