// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	wslOSReleasePath  = "/proc/sys/kernel/osrelease"
	mountInfoPath     = "/proc/self/mountinfo"
	ollamaProbePath   = "/api/version"
	ollamaProbeWait   = 2 * time.Second
	wslOllamaLocalFix = "If the configured endpoint is intentionally remote, keep it and restrict access to that API. " +
		"For Ollama on this Windows host, quit Ollama from the tray, then run in PowerShell:\n" +
		"  [Environment]::SetEnvironmentVariable('OLLAMA_HOST', '127.0.0.1:11434', 'User')\n" +
		"Start Ollama again from the Windows Start menu. " +
		"WSL2 NAT cannot reach Windows loopback directly. On Windows 11 22H2 or newer, open PowerShell and run:\n" +
		"  notepad \"$env:USERPROFILE\\.wslconfig\"\n" +
		"Merge these settings into that file (preserve other settings):\n" +
		"  [wsl2]\n  networkingMode=mirrored\n" +
		"Save, then run in PowerShell (stops all WSL sessions):\n" +
		"  wsl --shutdown\n" +
		"Reopen WSL and check:\n" +
		"  curl --fail --show-error http://127.0.0.1:11434/api/version\n" +
		"Then rerun your Gateway command with --inference-ollama-endpoint http://127.0.0.1:11434. " +
		"For Ollama inside WSL, start it there bound to loopback."
)

// Environment describes the host the CLI is running on in the terms that
// change how a first run must be set up.
type Environment struct {
	// WSL is true when the Linux CLI runs inside Windows Subsystem for Linux.
	WSL bool
}

// DetectEnvironment inspects the running host.
func DetectEnvironment() Environment {
	return Environment{WSL: runtime.GOOS == "linux" && osReleaseIsWSL(readFileString(wslOSReleasePath))}
}

// osReleaseIsWSL reports whether a kernel osrelease string identifies WSL
// (for example "6.6.114.1-microsoft-standard-WSL2").
func osReleaseIsWSL(osRelease string) bool {
	lower := strings.ToLower(osRelease)
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// Advisory is one first-run finding with the action that resolves it.
type Advisory struct {
	Problem string
	Fix     string
}

// OllamaAdvisoryInput carries the resolved inference settings to check.
type OllamaAdvisoryInput struct {
	// Endpoint is the resolved Ollama endpoint ("" means the loopback default).
	Endpoint string
	// ModelStorageRoot is the --model-storage-root value, possibly empty.
	ModelStorageRoot string
}

// OllamaAdvisories returns the first-run problems with the Ollama setup that
// are specific to WSL with Ollama running on the Windows side. It returns
// nothing outside WSL.
func (e Environment) OllamaAdvisories(ctx context.Context, in OllamaAdvisoryInput) []Advisory {
	if !e.WSL {
		return nil
	}
	return wslOllamaAdvisories(ctx, in, defaultOllamaReachable, readFileString(mountInfoPath))
}

func wslOllamaAdvisories(ctx context.Context, in OllamaAdvisoryInput, reachable func(context.Context, string) bool, mountInfo string) []Advisory {
	var out []Advisory
	endpoint := strings.TrimSpace(in.Endpoint)
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}

	if !reachable(ctx, endpoint) {
		out = append(out, Advisory{
			Problem: fmt.Sprintf("Ollama is not reachable at %s from WSL.", endpoint),
			Fix:     wslOllamaLocalFix,
		})
	} else if hostIsNonLoopback(endpoint) {
		out = append(out, Advisory{
			Problem: fmt.Sprintf("Ollama endpoint %s is not loopback. Confirm that remote API access is intentional; the endpoint alone does not identify the Windows host or its bind settings.", endpoint),
			Fix:     wslOllamaLocalFix,
		})
	}

	if root := strings.TrimSpace(in.ModelStorageRoot); root != "" && onWindowsMount(mountInfo, root) {
		out = append(out, Advisory{
			Problem: fmt.Sprintf("--model-storage-root %s is on a Windows drive (9p mount); hashing model weights for provenance will be slow.", root),
			Fix:     "Expect a long first pass, or keep a copy of the model store on the WSL filesystem.",
		})
	}
	return out
}

func hostIsNonLoopback(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func defaultOllamaReachable(ctx context.Context, endpoint string) bool {
	ctx, cancel := context.WithTimeout(ctx, ollamaProbeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+ollamaProbePath, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// onWindowsMount reports whether path lies under a drvfs/9p mount listed in
// mountinfo (the /mnt/<drive> mounts WSL creates for Windows drives).
func onWindowsMount(mountInfo, path string) bool {
	for _, line := range strings.Split(mountInfo, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		fsFields := strings.Fields(after)
		if len(fsFields) == 0 || (fsFields[0] != "9p" && fsFields[0] != "drvfs") {
			continue
		}
		fields := strings.Fields(before)
		if len(fields) < 5 {
			continue
		}
		mountPoint := fields[4]
		if path == mountPoint || strings.HasPrefix(path, strings.TrimRight(mountPoint, "/")+"/") {
			return true
		}
	}
	return false
}

func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
