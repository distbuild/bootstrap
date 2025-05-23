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
	aospPath       string
	distbuildPath  string
	scpPassword    string
	workerEndpoint string
)

var rootCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "boong bootstrap",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		if err := run(ctx); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Error:", err.Error())
			os.Exit(1)
		}
	},
}

// nolint:gochecknoinits
func init() {
	rootCmd.Flags().StringVarP(&scpPassword, "password", "p", "userpasswd", "SCP password")
	rootCmd.Flags().StringVarP(&workerEndpoint, "worker", "w", "user@worker_ip", "Worker endpoint")
	rootCmd.Flags().StringVar(&aospPath, "aosp-path", "", "AOSP base path")
	rootCmd.Flags().StringVar(&distbuildPath, "distbuild-path", "", "Distbuild binaries path")

	_ = cobra.MarkFlagRequired(rootCmd.Flags(), "aosp-path")
	_ = cobra.MarkFlagRequired(rootCmd.Flags(), "distbuild-path")

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

	if err := cloneDistbuildRepo(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}

	if err := downloadResources(); err != nil {
		return fmt.Errorf("download resources failed: %w", err)
	}

	if err := transferAgent(); err != nil {
		return fmt.Errorf("transfer agent failed: %w", err)
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
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %v [%s]", err, filepath.Base(filePath))
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

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

func transferAgent() error {
	sourcePath := filepath.Join(distbuildPath, "boong", "bin", "agent")
	cmd := exec.Command("sshpass", "-p", scpPassword, "scp",
		"-o", "StrictHostKeyChecking=no",
		sourcePath,
		fmt.Sprintf("%s:~/", workerEndpoint))

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v\n%s", err, stderr.String())
	}

	return nil
}
