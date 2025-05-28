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

	if err := createSymlinks(); err != nil {
		return fmt.Errorf("create symlinks failed: %w", err)
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

	cmd := exec.Command("git", "clone", repo, targetPath)
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
		if err := downloadFile(proxyBin, filepath.Join(binDir, "proxy")); err != nil {
			return fmt.Errorf("download proxy binary failed: %w", err)
		}
	} else {
		fmt.Println("warning: environment variable PROXY_BIN not set")
	}

	distninjaBin, exists := os.LookupEnv("DISTNINJA_BIN")
	if exists && distninjaBin != "" {
		if err := downloadFile(distninjaBin, filepath.Join(binDir, "distninja")); err != nil {
			return fmt.Errorf("download distninja binary failed: %w", err)
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
