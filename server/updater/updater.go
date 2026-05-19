package updater

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	adminDomain     = "https://sre-admin.okg.com"
	checkInterval   = 30 * time.Minute
	httpTimeout     = 30 * time.Second
	downloadTimeout = 5 * time.Minute
)

// Updater manages background self-update for the beehub binary.
type Updater struct {
	localVersion string
	dataDir      string
	ticker       *time.Ticker
	stopCh       chan struct{}
	onReady      func(remoteVersion string)
}

// New creates an Updater. dataDir is used for temporary download files.
// onReady is called once a new binary has been downloaded and replaced on disk.
func New(localVersion, dataDir string, onReady func(remoteVersion string)) *Updater {
	return &Updater{
		localVersion: localVersion,
		dataDir:      dataDir,
		stopCh:       make(chan struct{}),
		onReady:      onReady,
	}
}

// Start launches the background check loop. The first check runs after a 30s delay.
func (u *Updater) Start() {
	u.ticker = time.NewTicker(checkInterval)
	go func() {
		select {
		case <-time.After(30 * time.Second):
			u.checkAndUpdate()
		case <-u.stopCh:
			return
		}
		for {
			select {
			case <-u.ticker.C:
				u.checkAndUpdate()
			case <-u.stopCh:
				return
			}
		}
	}()
}

// Stop shuts down the background loop.
func (u *Updater) Stop() {
	if u.ticker != nil {
		u.ticker.Stop()
	}
	close(u.stopCh)
}

// Restart replaces the running process with the new binary via syscall.Exec.
func (u *Updater) Restart() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	log.Printf("[updater] restarting via syscall.Exec: %s", execPath)
	return syscall.Exec(execPath, os.Args, os.Environ())
}

// --- internal ---

func (u *Updater) checkAndUpdate() {
	if u.localVersion == "" || u.localVersion == "dev" {
		return
	}

	remoteVersion, err := u.getRemoteVersion()
	if err != nil {
		log.Printf("[updater] version check failed: %v", err)
		return
	}

	if remoteVersion <= u.localVersion {
		log.Printf("[updater] up to date (local=%s remote=%s)", u.localVersion, remoteVersion)
		return
	}

	log.Printf("[updater] new version available: %s → %s, downloading...", u.localVersion, remoteVersion)

	if err := u.downloadAndReplace(); err != nil {
		log.Printf("[updater] download/replace failed: %v", err)
		return
	}

	log.Printf("[updater] binary replaced, ready to restart")
	u.ticker.Stop() // no need to check again
	if u.onReady != nil {
		u.onReady(remoteVersion)
	}
}

func (u *Updater) getRemoteVersion() (string, error) {
	url := fmt.Sprintf("%s/rca/api/v1/oss/get_nexbox_ver?platform=%s&arch=%s",
		adminDomain, runtime.GOOS, runtime.GOARCH)
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func (u *Updater) downloadAndReplace() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	url := fmt.Sprintf("%s/rca/api/v1/oss/get_nexbox?platform=%s&arch=%s",
		adminDomain, runtime.GOOS, runtime.GOARCH)

	tmpDir := filepath.Join(u.dataDir, fmt.Sprintf("beehub-update-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	gzPath := filepath.Join(tmpDir, "beehub.gz")
	if err := u.downloadFile(url, gzPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	newPath := filepath.Join(tmpDir, "beehub")
	if err := decompressGzip(gzPath, newPath); err != nil {
		return fmt.Errorf("decompress: %w", err)
	}

	if err := os.Chmod(newPath, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	if err := os.Rename(newPath, execPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

func (u *Updater) downloadFile(url, dest string) error {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func decompressGzip(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	gz, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer gz.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, gz)
	return err
}
