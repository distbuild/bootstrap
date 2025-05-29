package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

//go:embed .env
var envFile string

var (
	BuildTime string
	CommitID  string
)

var (
	aospPath         string
	distbuildPath    string
	deployAgent      bool
	enableToolchains bool
)

var rootCmd = &cobra.Command{
	Use:     "bootstrap",
	Short:   "boong bootstrap",
	Version: BuildTime + "-" + CommitID,
	Run: func(cmd *cobra.Command, args []string) {
		if err := checkFlags(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error:", err.Error())
			os.Exit(1)
		}
		ctx := context.Background()
		if err := run(ctx); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error:", err.Error())
			os.Exit(1)
		}
	},
}

// nolint:gochecknoinits
func init() {
	rootCmd.Flags().StringVar(&aospPath, "aosp-path", "", "aosp base path")
	rootCmd.Flags().StringVar(&distbuildPath, "distbuild-path", "", "distbuild binaries path")
	rootCmd.Flags().BoolVar(&deployAgent, "deploy-agent", false, "deploy agent service")
	rootCmd.Flags().BoolVar(&enableToolchains, "enable-toolchains", false, "download prebuilt toolchains")

	_ = rootCmd.MarkFlagRequired("distbuild-path")
	rootCmd.MarkFlagsMutuallyExclusive("aosp-path", "deploy-agent")

	rootCmd.Root().CompletionOptions.DisableDefaultCmd = true
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(_ context.Context) error {
	if err := loadEnvFile(envFile); err != nil {
		return fmt.Errorf("load .env failed: %w", err)
	}

	if deployAgent {
		if err := downloadAgent(); err != nil {
			return fmt.Errorf("download agent failed: %w", err)
		}
		fmt.Println("starting agent in background...")
		return runAgent()
	}

	if err := cloneDistbuildRepo(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}

	if err := downloadResources(); err != nil {
		return fmt.Errorf("download resources failed: %w", err)
	}

	if enableToolchains {
		if err := downloadToolchains(); err != nil {
			return fmt.Errorf("download toolchains failed: %w", err)
		}
	}

	return nil
}

func checkFlags() error {
	var err error

	if aospPath == "" && !deployAgent {
		return fmt.Errorf("--aosp-path or --deploy-agent flag is required")
	}

	aospPath, err = expandTildeIfPresent(aospPath)
	if err != nil {
		return fmt.Errorf("failed to expand tilde: %w", err)
	}

	distbuildPath, err = expandTildeIfPresent(distbuildPath)
	if err != nil {
		return fmt.Errorf("failed to expand tilde: %w", err)
	}

	return nil
}

func expandTildeIfPresent(path string) (string, error) {
	if !strings.Contains(path, "~") {
		return path, nil
	}

	if strings.HasPrefix(path, "~") {
		usr, err := user.Current()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return usr.HomeDir, nil
		}
		if strings.HasPrefix(path, "~/") {
			return filepath.Join(usr.HomeDir, path[2:]), nil
		}
	}

	return path, nil
}

func loadEnvFile(content string) error {
	scanner := bufio.NewScanner(strings.NewReader(content))

	for scanner.Scan() {
		line := scanner.Text()
		// Skip comments or empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if _, ok := os.LookupEnv(key); !ok {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}

	return nil
}

func cloneDistbuildRepo() error {
	targetPath := filepath.Join(aospPath, "build", "distbuild")

	if err := os.RemoveAll(targetPath); err != nil {
		return fmt.Errorf("failed to remove existing distbuild directory: %w", err)
	}

	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return fmt.Errorf("create directory failed: %w", err)
	}

	host, exists := os.LookupEnv("REPO_HOST")
	if !exists || host == "" {
		return fmt.Errorf("environment variable REPO_HOST not set")
	}

	repo, exists := os.LookupEnv("DISTBUILD_REPO")
	if !exists || repo == "" {
		repo, exists = os.LookupEnv("WRAPPER_REPO")
		if !exists || repo == "" {
			return fmt.Errorf("environment variable DISTBUILD_REPO or WRAPPER_REPO not set")
		}
		targetPath = filepath.Join(targetPath, "boong")
		_ = os.MkdirAll(targetPath, 0755)
		targetPath = filepath.Join(targetPath, "wrapper")
	}

	bar, done, _ := runProgress("clone repo...")
	defer func(bar *progressbar.ProgressBar, done chan bool) {
		_ = stopProgress(bar, done)
	}(bar, done)

	cmd := exec.Command("git", "clone", fmt.Sprintf("%s/%s", host, repo), targetPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v\n%s", err, stderr.String())
	}

	return nil
}

func downloadAgent() error {
	binDir := filepath.Join(distbuildPath, "boong", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("create bin directory failed: %w", err)
	}

	agentBin, exists := os.LookupEnv("AGENT_BIN")
	if !exists || agentBin == "" {
		fmt.Println("warning: environment variable AGENT_BIN not set")
		return nil
	}

	bar, done, _ := runProgress("download agent...")
	defer func(bar *progressbar.ProgressBar, done chan bool) {
		_ = stopProgress(bar, done)
	}(bar, done)

	return downloadFile(
		agentBin,
		filepath.Join(binDir, "agent"),
	)
}

func runAgent() error {
	agentPath := filepath.Join(distbuildPath, "boong", "bin", "agent")
	cmd := createAgentCommand(agentPath)

	logFile, err := os.Create(filepath.Join(distbuildPath, "agent.log"))
	if err != nil {
		return fmt.Errorf("create log file failed: %w", err)
	}

	defer func(logFile *os.File) {
		_ = logFile.Close()
	}(logFile)

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agent startup failed: %w", err)
	}

	fmt.Printf("agent started with PID %d\n", cmd.Process.Pid)
	fmt.Printf("log output: %s\n", logFile.Name())

	return nil
}

func downloadResources() error {
	binDir := filepath.Join(distbuildPath, "boong", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("create bin directory failed: %w", err)
	}

	proxyBin, exists := os.LookupEnv("PROXY_BIN")
	if exists && proxyBin != "" {
		bar, done, _ := runProgress("download proxy...")
		defer func(bar *progressbar.ProgressBar, done chan bool) {
			_ = stopProgress(bar, done)
		}(bar, done)
		if err := downloadFile(proxyBin, filepath.Join(binDir, "proxy")); err != nil {
			return fmt.Errorf("download proxy binary failed: %w", err)
		}
		if err := createSymlinks("proxy"); err != nil {
			return fmt.Errorf("create symlinks failed: %w", err)
		}
	} else {
		fmt.Println("warning: environment variable PROXY_BIN not set")
	}

	distninjaBin, exists := os.LookupEnv("DISTNINJA_BIN")
	if exists && distninjaBin != "" {
		bar, done, _ := runProgress("download distninja...")
		defer func(bar *progressbar.ProgressBar, done chan bool) {
			_ = stopProgress(bar, done)
		}(bar, done)
		if err := downloadFile(distninjaBin, filepath.Join(binDir, "distninja")); err != nil {
			return fmt.Errorf("download distninja binary failed: %w", err)
		}
		if err := createSymlinks("distninja"); err != nil {
			return fmt.Errorf("create symlinks failed: %w", err)
		}
	} else {
		fmt.Println("warning: environment variable DISTNINJA_BIN not set")
	}

	return nil
}

func downloadFile(url, filePath string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request failed: %v [%s]", err, filepath.Base(filePath))
	}

	username := os.Getenv("AUTH_USER")
	password := os.Getenv("AUTH_PASS")

	if username != "" && password != "" {
		req.SetBasicAuth(username, password)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %v [%s]", err, filepath.Base(filePath))
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status code %d [%s]", resp.StatusCode, filepath.Base(filePath))
	}

	out, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("create file failed: %v [%s]", err, filepath.Base(filePath))
	}

	defer func(out *os.File) {
		_ = out.Close()
	}(out)

	if _, err = io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("write file failed: %v [%s]", err, filepath.Base(filePath))
	}

	if err := os.Chmod(filePath, 0755); err != nil {
		return fmt.Errorf("chmod failed: %v [%s]", err, filepath.Base(filePath))
	}

	return nil
}

func createSymlinks(name string) error {
	source := filepath.Join(distbuildPath, "boong", "bin", name)
	target := filepath.Join("/usr/local/bin", name)

	cmd := exec.Command("sudo", "ln", "-sf", source, target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create symlink failed: %v [%s]", err, filepath.Base(name))
	}

	return nil
}

func downloadToolchains() error {
	host, exists := os.LookupEnv("REPO_HOST")
	if !exists || host == "" {
		return fmt.Errorf("environment variable REPO_HOST not set")
	}

	toolchains := []struct {
		name string
		repo string
		path string
	}{
		{
			name: "clang",
			repo: fmt.Sprintf("%s/platform/prebuilts/clang/host/linux-x86", host),
			path: filepath.Join(distbuildPath, "prebuilts/clang/host/linux-x86"),
		},
		{
			name: "gcc",
			repo: fmt.Sprintf("%s/platform/prebuilts/gcc/linux-x86/host/x86_64-linux-glibc2.17-4.8", host),
			path: filepath.Join(distbuildPath, "prebuilts/gcc/linux-x86/host/x86_64-linux-glibc2.17-4.8"),
		},
	}

	for _, tc := range toolchains {
		if err := cloneToolchain(tc.repo, tc.path, tc.name); err != nil {
			return err
		}
	}

	return nil
}

func cloneToolchain(repo, path, name string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("failed to remove existing %s directory: %w", name, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create directory for %s failed: %w", name, err)
	}

	bar, done, _ := runProgress(fmt.Sprintf("clone %s...", name))
	defer func(bar *progressbar.ProgressBar, done chan bool) {
		_ = stopProgress(bar, done)
	}(bar, done)

	cmd := exec.Command("git", "clone", repo, "-b", "master", "--depth", "1", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s clone failed: %v\n%s", name, err, stderr.String())
	}

	return nil
}

func runProgress(description string) (*progressbar.ProgressBar, chan bool, error) {
	bar := progressbar.NewOptions(-1,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerHead:    ">",
			SaucerPadding: "",
			BarStart:      "[",
			BarEnd:        "]",
		}),
		progressbar.OptionOnCompletion(func() {
			fmt.Println("")
		}),
	)

	done := make(chan bool)

	go func() {
		for {
			select {
			case <-done:
				return
			default:
				_ = bar.Add(1)
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	return bar, done, nil
}

func stopProgress(bar *progressbar.ProgressBar, done chan bool) error {
	done <- true

	return bar.Finish()
}
