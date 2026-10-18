// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/spf13/cobra"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/cli/stream"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/ollama"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

type operatorListEntry struct {
	OperatorID                 string                         `json:"operator_id"`
	OperatorSessionID          string                         `json:"operator_session_id"`
	OperatorType               constants.OperatorType         `json:"operator_type"`
	OperatorCapabilities       constants.OperatorCapabilities `json:"operator_capabilities"`
	Status                     constants.OperatorStatus       `json:"status"`
	Component                  constants.ComponentName        `json:"component"`
	Hostname                   string                         `json:"hostname,omitempty"`
	Name                       string                         `json:"name,omitempty"`
	LastHeartbeatAt            *time.Time                     `json:"last_heartbeat_at,omitempty"`
	InferenceOllamaEndpoint    string                         `json:"inference_ollama_endpoint,omitempty"`
	ProvenanceModelStorageRoot string                         `json:"provenance_operator_model_storage_root,omitempty"`
}

type operatorListOutput struct {
	Operators []operatorListEntry `json:"operators"`
}

func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "operator",
		Aliases: []string{"operators"},
		Short:   "Manage Operator instances",
		Long:    `Manage and view ztd Operator instances connected to the Gateway.`,
	}

	cmd.AddCommand(
		operatorListCmd(),
		operatorShowCmd(),
		operatorBindCmd(),
		operatorRunCmd(),
		operatorStopCmd(),
		operatorResetIdentityCmd(),
		operatorStartCmd(),
		operatorCpCmd(),
		operatorScpCmd(),
		operatorDeployCmd(),
		operatorDeploymentStateCmd(),
		operatorGatewayPreflightCmd(),
		operatorStreamCmd(),
		operatorModelCmd(),
		operatorDeployHostCmd(),
	)

	return cmd
}

func operatorModelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "model", Hidden: true}
	cmd.PersistentFlags().String(operatorModelOllamaEndpointFlag, "", "Ollama provider endpoint for model maintenance (default: ZTD_OLLAMA_ENDPOINT; required, e.g. http://127.0.0.1:11434)")
	cmd.AddCommand(
		operatorModelPullCmd(),
		operatorModelCopyCmd(),
		operatorModelInventoryCmd(),
		operatorModelResidencyCmd(),
		operatorModelReleaseCmd(),
	)
	return cmd
}

const operatorModelOllamaEndpointFlag = "inference-ollama-endpoint"

// operatorOllamaEndpoint resolves the Ollama provider endpoint from the model
// maintenance persistent flag, else ZTD_OLLAMA_ENDPOINT. It fails closed when
// both are blank.
func operatorOllamaEndpoint(cmd *cobra.Command) (string, error) {
	raw, err := cmd.Flags().GetString(operatorModelOllamaEndpointFlag)
	if err != nil {
		return "", fmt.Errorf("operator: model maintenance: %w", err)
	}
	endpoint := config.InferenceOllamaEndpoint(raw)
	if endpoint == "" {
		return "", fmt.Errorf("operator: model maintenance: %w", constants.ErrInferenceEndpointInvalid)
	}
	return endpoint, nil
}

func operatorModelPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "pull <model>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint, err := operatorOllamaEndpoint(cmd)
			if err != nil {
				return err
			}
			base, err := url.Parse(endpoint)
			if err != nil {
				return fmt.Errorf("operator: pull model: %w", err)
			}
			client := ollama.NewClient(base, &http.Client{Timeout: inference.ProviderRequestTimeout})
			if err := client.Pull(cmd.Context(), args[0], nil); err != nil {
				return fmt.Errorf("operator: pull model: %w", err)
			}
			return nil
		},
	}
}

func operatorModelCopyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "copy <source> <destination>",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint, err := operatorOllamaEndpoint(cmd)
			if err != nil {
				return err
			}
			base, err := url.Parse(endpoint)
			if err != nil {
				return fmt.Errorf("operator: copy model: %w", err)
			}
			client := ollama.NewClient(base, &http.Client{Timeout: inference.ProviderRequestTimeout})
			if err := client.Copy(cmd.Context(), args[0], args[1]); err != nil {
				return fmt.Errorf("operator: copy model: %w", err)
			}
			return nil
		},
	}
}

func operatorModelInventoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "inventory",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint, err := operatorOllamaEndpoint(cmd)
			if err != nil {
				return err
			}
			backend, err := inference.NewOllamaBackend(endpoint, slog.Default())
			if err != nil {
				return fmt.Errorf("operator: model inventory: %w", err)
			}
			entries, err := backend.ListProviderModelInventory(cmd.Context())
			if err != nil {
				return fmt.Errorf("operator: model inventory: %w", err)
			}
			payload, err := json.Marshal(entries)
			if err != nil {
				return fmt.Errorf("operator: model inventory: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(payload))
			return err
		},
	}
}

func operatorModelResidencyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "residency",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint, err := operatorOllamaEndpoint(cmd)
			if err != nil {
				return err
			}
			backend, err := inference.NewOllamaBackend(endpoint, slog.Default())
			if err != nil {
				return fmt.Errorf("operator: model residency: %w", err)
			}
			residency, err := backend.ReadResidency(cmd.Context())
			if err != nil {
				return fmt.Errorf("operator: model residency: %w", err)
			}
			payload, err := json.Marshal(residency)
			if err != nil {
				return fmt.Errorf("operator: model residency: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(payload))
			return err
		},
	}
}

func operatorModelReleaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "release <model>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint, err := operatorOllamaEndpoint(cmd)
			if err != nil {
				return err
			}
			backend, err := inference.NewOllamaBackend(endpoint, slog.Default())
			if err != nil {
				return fmt.Errorf("operator: release model: %w", err)
			}
			if err := backend.ReleaseModel(cmd.Context(), args[0]); err != nil {
				return fmt.Errorf("operator: release model: %w", err)
			}
			return nil
		},
	}
}

func operatorListCmd() *cobra.Command {
	return operatorListCmdWithConfig(shared.LoadConfig, authcmd.DefaultAPIClientFactory, shared.NewFileSvc)
}

func operatorListCmdWithConfig(configLoader func(string) (*config.Config, error), clientFactory authcmd.APIClientFactory, fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all Operator instances",
		Long:  `List all Operator instances currently connected to the Gateway.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := configLoader("")
			if err != nil {
				return err
			}

			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			creds, err := auth.LoadCredentials(fileSvc, cfg)
			if err != nil || creds == nil {
				return fmt.Errorf("%w\nEnroll first with:\n./ztd auth enroll user", constants.ErrNotAuthenticated)
			}

			client, err := clientFactory(fileSvc, cfg)
			if err != nil {
				return err
			}

			resp, err := client.Get(constants.APIPaths.Operators + "?user_id=" + creds.UserID)
			if err != nil {
				return err
			}

			var slotResp models.OperatorSlotResponse
			if err := json.Unmarshal(resp, &slotResp); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
			}

			operators := slotResp.Operators
			if len(operators) == 0 {
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), operatorListOutput{Operators: []operatorListEntry{}})
				}
				cmd.Println("No operators found")
				return nil
			}

			if output.JSONEnabled(cmd) {
				entries := make([]operatorListEntry, 0, len(operators))
				for _, op := range operators {
					entry := operatorListEntry{
						OperatorID:           op.Id,
						OperatorSessionID:    op.OperatorSessionId,
						OperatorType:         constants.OperatorType(op.OperatorType),
						OperatorCapabilities: operatorcapability.GetOperatorCapabilities(op),
						Status:               constants.OperatorStatus(op.Status),
						Component:            constants.ComponentName(op.Component),
						Hostname:             operatorHostnameValue(op),
					}
					if op.LastHeartbeatAt != nil {
						heartbeatAt := op.LastHeartbeatAt.AsTime()
						entry.LastHeartbeatAt = &heartbeatAt
					}
					if op.RuntimeConfig != nil {
						entry.InferenceOllamaEndpoint = op.RuntimeConfig.InferenceOllamaEndpoint
						entry.ProvenanceModelStorageRoot = op.RuntimeConfig.ProvenanceOperatorModelStorageRoot
					}
					entries = append(entries, entry)
				}
				return output.WriteJSON(cmd.OutOrStdout(), operatorListOutput{Operators: entries})
			}

			cmd.Printf("Operators (%d total)\n", len(operators))
			cmd.Println(strings.Repeat("=", operatorTableWidth))
			printOperatorTable(cmd, operators)
			return nil
		},
	}
	return cmd
}

// resolveOperatorWorkingDir expands a leading ~ and makes a non-blank working
// directory absolute. cmd.exe, PowerShell, and GNU make on Windows pass ~ to
// native commands unexpanded, and the Gateway records the directory as the
// Operator's local_dir, so the CLI resolves it rather than the shell.
func resolveOperatorWorkingDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", nil
	}
	expanded, err := resolveHomePath(dir)
	if err != nil {
		return "", err
	}
	return filepath.Abs(expanded)
}

func operatorStartCmd() *cobra.Command {
	var trustBundle string
	var workingDir string
	var deploymentID string
	var cloud bool
	var provider string
	var logLevel string
	var heartbeatInterval int
	var gatewayHTTPPort int
	var gatewayHTTPSPort int
	var capabilities constants.OperatorCapabilities
	var inferenceOllamaEndpoint string
	var inferenceKeepAlive string
	var providerBoundaryObserverID string
	var provenanceOperatorID string
	var provenanceOperatorModelStorageRoot string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the ztd Operator in foreground (worker mode)",
		Long:  `Start the ztd Operator in foreground as a worker. This connects to the Gateway and executes commands. This is the re-exec target for remote deployment and can also be run directly for debugging.`,
		// Anything after "--" is positional and would otherwise be silently
		// dropped, starting the Operator without the intended flags.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateHeartbeatInterval(heartbeatInterval); err != nil {
				return err
			}
			if err := validateGatewayPort("gateway-http-port", gatewayHTTPPort); err != nil {
				return err
			}
			if err := validateGatewayPort("gateway-https-port", gatewayHTTPSPort); err != nil {
				return err
			}
			workingDir, err := resolveOperatorWorkingDir(workingDir)
			if err != nil {
				return fmt.Errorf("operator start: --working-dir: %w", err)
			}
			endpoint, _ := cmd.Flags().GetString("endpoint")
			opts := serve.ServeOperatorOptions{
				OperatorCapabilities:       capabilities,
				LogLevel:                   logLevel,
				Endpoint:                   endpoint,
				HTTPPort:                   gatewayHTTPPort,
				HTTPSPort:                  gatewayHTTPSPort,
				TrustBundlePath:            trustBundle,
				WorkingDir:                 workingDir,
				DeploymentID:               deploymentID,
				LaunchDir:                  workingDir,
				CloudMode:                  cloud,
				CloudProvider:              provider,
				HeartbeatInterval:          time.Duration(heartbeatInterval) * time.Second,
				InferenceOllamaEndpoint:    config.InferenceOllamaEndpoint(inferenceOllamaEndpoint),
				InferenceKeepAlive:         inferenceKeepAlive,
				ProviderBoundaryObserverID: providerBoundaryObserverID,

				ProvenanceOperatorID:               provenanceOperatorID,
				ProvenanceOperatorModelStorageRoot: provenanceOperatorModelStorageRoot,
			}

			// Run operator (this blocks until shutdown)
			serve.RunOperator(opts, shared.VersionInfoFromCmd(cmd))
			return nil
		},
	}

	cmd.Flags().StringVar(&trustBundle, "trust-bundle", "", "Path to an explicit CA trust bundle (read-only; overrides the bundle delivered at enrollment)")
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "Working directory for command execution")
	cmd.Flags().StringVar(&deploymentID, "deployment-id", "", "Launch identifier for structured deployment progress")
	_ = cmd.Flags().MarkHidden("deployment-id")
	cmd.Flags().BoolVarP(&cloud, "cloud", "c", false, "Cloud operator mode")
	cmd.Flags().StringVar(&provider, "provider", "", "Cloud provider (aws, gcp, azure)")
	cmd.Flags().StringVarP(&logLevel, "log", "l", "info", "Log level: info, error, debug")
	cmd.Flags().IntVar(&heartbeatInterval, "heartbeat-interval", 30, "Heartbeat send interval in seconds (0-300; 0 selects 30; declared to the Gateway at session start, which marks the Operator stale after twice this interval, minimum 60 seconds)")
	cmd.Flags().IntVar(&gatewayHTTPPort, "gateway-http-port", 0, "Gateway HTTP discovery port to dial (default: platform default)")
	cmd.Flags().IntVar(&gatewayHTTPSPort, "gateway-https-port", 0, "Gateway HTTPS/mTLS port to dial (default: platform default)")

	// Inference (ztdllama) flags. Enable when the operator runs as an
	// Inference Node calling the configured remote Ollama provider.
	cmd.Flags().Var(&capabilities, "capabilities", "Comma-separated operator capabilities: data,inference,provenance,observer (repeatable; default: data)")
	cmd.Flags().StringVar(&inferenceOllamaEndpoint, "inference-ollama-endpoint", "", "Remote Ollama provider endpoint (default: ZTD_OLLAMA_ENDPOINT, else http://127.0.0.1:11434)")
	cmd.Flags().StringVar(&inferenceKeepAlive, "inference-keep-alive", "", fmt.Sprintf("Ollama keep-alive duration (default: %s for infinite)", constants.InferenceDefaultKeepAlive))
	cmd.Flags().StringVar(&providerBoundaryObserverID, "provider-boundary-observer-id", "", "Stable observer identity pseudonym")
	cmd.Flags().StringVar(&provenanceOperatorID, "provenance-operator-id", "", "Stable provenance operator identity pseudonym")
	cmd.Flags().StringVar(&provenanceOperatorModelStorageRoot, "model-storage-root", "", "Root directory containing content-addressed model weight blobs (for example ~/.ollama/models)")

	return cmd
}

// validateHeartbeatInterval rejects heartbeat intervals the Gateway would
// refuse at session start. The Operator declares its interval and the Gateway
// derives the stale deadline from it, up to
// constants.OperatorHeartbeatMaxInterval. Zero selects the default.
func validateHeartbeatInterval(seconds int) error {
	interval := time.Duration(seconds) * time.Second
	if interval < 0 || interval > constants.OperatorHeartbeatMaxInterval {
		return fmt.Errorf("%w: %s is outside 0 (default) to %s",
			constants.ErrOperatorHeartbeatIntervalInvalid, interval, constants.OperatorHeartbeatMaxInterval)
	}
	return nil
}

func validateGatewayPort(flag string, port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("--%s %d: %w", flag, port, constants.ErrOperatorGatewayPortInvalid)
	}
	return nil
}

func operatorCpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cp <target>",
		Short: "Copy the operator binary to a target location",
		Long:  `Copy the ztd operator binary to a specified directory or file. If a directory is provided, the binary will be copied with its default name. If a filename is provided, the binary will be copied with that name.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			sourceBinary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrStatFailed, err)
			}

			if _, err := os.Stat(sourceBinary); os.IsNotExist(err) {
				return fmt.Errorf("%w: %s", constants.ErrPathNotFound, sourceBinary)
			}

			targetInfo, err := os.Stat(target)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("%w: %w", constants.ErrStatFailed, err)
			}

			var destPath string
			if err == nil && targetInfo.IsDir() {
				basename := filepath.Base(sourceBinary)
				destPath = filepath.Join(target, basename)
			} else {
				destPath = target
			}

			if err := CopyFile(sourceBinary, destPath); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrPathValidation, err)
			}

			cmd.Printf("Copied operator binary to %s\n", destPath)
			return nil
		},
	}
	return cmd
}

func operatorScpCmd() *cobra.Command {
	var port int
	var identityFile string
	var recursive bool
	var preserve bool
	var verbose bool
	var compression bool
	var prompt bool

	cmd := &cobra.Command{
		Use:   "scp <user@host:path>",
		Short: "Copy the operator binary to a remote host using scp",
		Long:  `Copy the ztd operator binary to a remote host using scp. Supports common scp flags. If the target path is a directory, the binary will be copied with its default name. Use --prompt to interactively configure scp options.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			sourceBinary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrStatFailed, err)
			}

			if _, err := os.Stat(sourceBinary); os.IsNotExist(err) {
				return fmt.Errorf("%w: %s", constants.ErrPathNotFound, sourceBinary)
			}

			if prompt {
				if err := promptForScpOptions(cmd, &port, &identityFile, &preserve, &verbose, &compression); err != nil {
					return err
				}
			}

			scpArgs := BuildScpArgs(port, identityFile, recursive, preserve, verbose, compression, sourceBinary, target)

			cmd.Printf("Copying operator binary to %s\n", target)
			if verbose {
				cmd.Printf("Command: scp %s\n", strings.Join(scpArgs, " "))
			}

			scpCmd := exec.Command("scp", scpArgs...)
			scpCmd.Stdout = cmd.OutOrStdout()
			scpCmd.Stderr = cmd.ErrOrStderr()
			scpCmd.Stdin = cmd.InOrStdin()

			if err := scpCmd.Run(); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrMCPRunShellCommandSSHDial, err)
			}

			cmd.Printf("Successfully copied operator binary to %s\n", target)
			return nil
		},
	}

	cmd.Flags().IntVarP(&port, "port", "P", 0, "Port to connect to on the remote host")
	cmd.Flags().StringVarP(&identityFile, "identity", "i", "", "Selects the file from which the identity (private key) for public key authentication is read")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Pass -r through to scp")
	cmd.Flags().BoolVar(&preserve, "preserve", false, "Preserves modification times, access times, and modes from the source file")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose mode")
	cmd.Flags().BoolVarP(&compression, "compression", "C", false, "Enable compression")
	cmd.Flags().BoolVar(&prompt, "prompt", false, "Prompt for scp options interactively")

	return cmd
}

func promptForScpOptions(cmd *cobra.Command, port *int, identityFile *string, preserve, verbose, compression *bool) error {
	reader := bufio.NewReader(cmd.InOrStdin())

	cmd.Println("\nSCP Configuration (press Enter to use default/skip):")

	if *port == 0 {
		cmd.Print("SSH Port [default: 22]: ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input != "" {
			var p int
			if _, err := fmt.Sscanf(input, "%d", &p); err == nil {
				*port = p
			}
		}
	}

	if *identityFile == "" {
		cmd.Print("Identity file path (SSH private key) [default: none]: ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input != "" {
			*identityFile = input
		}
	}

	cmd.Print("Preserve file attributes (times, modes) [y/N]: ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "y" || input == "Y" {
		*preserve = true
	}

	cmd.Print("Enable compression [y/N]: ")
	input, _ = reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "y" || input == "Y" {
		*compression = true
	}

	cmd.Print("Verbose output [y/N]: ")
	input, _ = reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "y" || input == "Y" {
		*verbose = true
	}

	cmd.Println()
	return nil
}

func BuildScpArgs(port int, identityFile string, recursive, preserve, verbose, compression bool, source, target string) []string {
	args := []string{}

	if port != 0 {
		args = append(args, "-P", fmt.Sprintf("%d", port))
	}

	if identityFile != "" {
		args = append(args, "-i", identityFile)
	}

	if recursive {
		args = append(args, "-r")
	}

	if preserve {
		args = append(args, "-p")
	}

	if verbose {
		args = append(args, "-v")
	}

	if compression {
		args = append(args, "-C")
	}

	args = append(args, source, target)
	return args
}

func CopyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.Chmod(dst, sourceInfo.Mode())
}

func operatorStreamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stream [host...] [flags]",
		Short: "Stream and execute the operator on remote hosts via SSH",
		Long:  `Stream the ztd operator binary via native Go crypto/ssh and execute it directly on remote hosts. Supports concurrent streaming, structured JSON output, and advanced SSH configuration. This is the canonical stream implementation (replaces the old exec.Command version).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Convert cobra args to the format expected by RunStream
			// RunStream expects args like ["host1", "host2", "--arch", "amd64", "--endpoint", "..."]
			stream.RunStream(args)
			return nil
		},
	}

	// Flags that match the native stream implementation
	cmd.Flags().String("arch", "amd64", "Target architecture: amd64, arm64, 386")
	cmd.Flags().String("hosts", "", "File of hosts (one per line) or - for stdin")
	cmd.Flags().Int("concurrency", 50, "Max parallel SSH sessions")
	cmd.Flags().Int("timeout", 60, "Per-host dial+inject timeout in seconds")
	cmd.Flags().StringP("endpoint", "e", "", "Platform endpoint: if set, starts Operator on each remote host")
	cmd.Flags().Int("gateway-http-port", constants.Ports.OperatorHttp, "Gateway HTTP discovery port to forward")
	cmd.Flags().Int("gateway-https-port", constants.Ports.OperatorHttps, "Gateway HTTPS/mTLS port to forward")
	cmd.Flags().String("ssh-config", "", "Path to SSH config file (default: ~/.ssh/config)")
	cmd.Flags().String("known-hosts", "", "Path to SSH known_hosts file (default: ~/.ssh/known_hosts)")
	cmd.Flags().String("binary-dir", "", "Directory containing arch-specific Operator builds")
	cmd.Flags().String("ssh-identity-file", "", "SSH identity file path")
	cmd.Flags().String("ssh-user", "", "SSH username")
	cmd.Flags().String("ssh-passphrase", "", "Passphrase for encrypted SSH private keys")
	cmd.Flags().Bool("preflight", false, "Enable pre-flight SSH connectivity check before binary transfer")

	return cmd
}
