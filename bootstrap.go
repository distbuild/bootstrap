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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed .env
var envFile string

var (
	BuildTime string
	CommitID  string
)

var (
	aospPath      string
	distbuildPath string
	deployAgent   bool
)

var rootCmd = &cobra.Command{
	Use:     "bootstrap",
	Short:   "boong bootstrap",
	Version: BuildTime + "-" + CommitID,
	Run: func(cmd *cobra.Command, args []string) {
		if aospPath == "" && !deployAgent {
			_, _ = fmt.Fprintln(os.Stderr, "please specify either --aosp-path or --deploy-agent flag")
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
		fmt.Println("Starting agent in background...")
		return runAgent()
	}

	if err := cloneDistbuildRepo(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}

	if err := downloadResources(); err != nil {
		return fmt.Errorf("download resources failed: %w", err)
	}

	if err := createSymlinks(); err != nil {
		return fmt.Errorf("create symlinks failed: %w", err)
	}

	return nil
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

		_ = os.Setenv(key, value)
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

	distbuildRepo, exists := os.LookupEnv("DISTBUILD_REPO")
	if !exists {
		return fmt.Errorf("environment variable DISTBUILD_REPO not set")
	}

	cmd := exec.Command("git", "clone", distbuildRepo, targetPath)
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
	if !exists {
		return fmt.Errorf("environment variable AGENT_BIN not set")
	}

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

	fmt.Printf("Agent started with PID %d\n", cmd.Process.Pid)
	fmt.Printf("Log output: %s\n", logFile.Name())

	return nil
}

func downloadResources() error {
	binDir := filepath.Join(distbuildPath, "boong", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("create bin directory failed: %w", err)
	}

	proxyBin, exists := os.LookupEnv("PROXY_BIN")
	if !exists {
		return fmt.Errorf("environment variable PROXY_BIN not set")
	}

	agentBin, exists := os.LookupEnv("AGENT_BIN")
	if !exists {
		return fmt.Errorf("environment variable AGENT_BIN not set")
	}

	distninjaBin, exists := os.LookupEnv("DISTNINJA_BIN")
	if !exists {
		return fmt.Errorf("environment variable DISTNINJA_BIN not set")
	}

	errChan := make(chan error, 3)

	go func() {
		errChan <- downloadFile(
			proxyBin,
			filepath.Join(binDir, "proxy"),
		)
	}()

	go func() {
		errChan <- downloadFile(
			agentBin,
			filepath.Join(binDir, "agent"),
		)
	}()

	go func() {
		errChan <- downloadFile(
			distninjaBin,
			filepath.Join(binDir, "distninja"),
		)
	}()

	for i := 0; i < 3; i++ {
		if err := <-errChan; err != nil {
			return err
		}
	}

	return nil
}

func downloadFile(url, filePath string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request failed: %v [%s]", err, filepath.Base(filePath))
	}

	username := os.Getenv("DISTBUILD_AUTH_USER")
	password := os.Getenv("DISTBUILD_AUTH_PASSWORD")
	if username == "" || password == "" {
		return fmt.Errorf("environment variables DISTBUILD_AUTH_USER or DISTBUILD_AUTH_PASSWORD not set [%s]", filepath.Base(filePath))
	}
	req.SetBasicAuth(username, password)

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

func createSymlinks() error {
	distninjaSrc := filepath.Join(distbuildPath, "boong", "bin", "distninja")
	proxySrc := filepath.Join(distbuildPath, "boong", "bin", "proxy")

	cmd := exec.Command("sudo", "ln", "-sf", distninjaSrc, "/usr/local/bin/distninja")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("distninja symlink failed: %v\n%s", err, stderr.String())
	}

	cmd = exec.Command("sudo", "ln", "-sf", proxySrc, "/usr/local/bin/proxy")
	stderr.Reset()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("proxy symlink failed: %v\n%s", err, stderr.String())
	}

	return nil
}
