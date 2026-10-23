// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestRunShellCommandTool_Name(t *testing.T) {
	tool := &RunShellCommandTool{}
	require.Equal(t, "run_shell_command", tool.Name())
}

// The description and schema comments are the usage contract a model reads
// from tools/list. They must say command is the executable name and args is
// the argv array, so a caller does not send a shell command line.
func TestRunShellCommandTool_DescriptionTeachesArgvContract(t *testing.T) {
	tool := &RunShellCommandTool{}
	desc := tool.Description()
	require.Contains(t, desc, "executable")
	require.Contains(t, desc, "command")
	require.Contains(t, desc, "args")
	require.NotContains(t, desc, "shell commands")

	schema := tool.InputSchema()
	command := schema.Properties["command"].Description
	require.Contains(t, command, "executable")
	require.Contains(t, command, "args")
	args := schema.Properties["args"].Description
	require.Contains(t, args, "argument")
	require.Contains(t, args, "command")
}

func TestRunShellCommandTool_InputSchema(t *testing.T) {
	tool := &RunShellCommandTool{}
	schema := tool.InputSchema()

	require.Equal(t, "object", schema.Type)
	require.NotNil(t, schema.Properties)

	// Check required fields
	require.Contains(t, schema.Required, "command")

	// Check command property
	cmdProp, ok := schema.Properties["command"]
	require.True(t, ok)
	require.Equal(t, "string", cmdProp.Type)

	// Check optional properties
	_, ok = schema.Properties["args"]
	require.True(t, ok)
	_, ok = schema.Properties["timeout"]
	require.True(t, ok)
	_, ok = schema.Properties["working_dir"]
	require.True(t, ok)
	_, ok = schema.Properties["hostnames"]
	require.True(t, ok)

	// Check hostnames property structure
	hostnamesProp, ok := schema.Properties["hostnames"]
	require.True(t, ok)
	require.Equal(t, "array", hostnamesProp.Type)
}

func TestRunShellCommandTool_Denylist_DangerousCommands(t *testing.T) {
	dangerousCommands := []string{
		"rm", "dd", "mkfs", "fdisk", "killall", "pkill",
		"reboot", "shutdown", "iptables", "mount", "umount",
		"passwd", "sudo", "su", "curl", "wget",
	}

	for _, cmd := range dangerousCommands {
		t.Run(cmd, func(t *testing.T) {
			err := validateCommandSafety(cmd, nil, "")
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), "blocked by safety policy")
		})
	}
}

func TestRunShellCommandTool_Denylist_DangerousPatterns(t *testing.T) {
	dangerousPatterns := []string{
		"mkfs.ext4",
		"> /dev/sda",
		"chmod 777 /",
		"nc -l 4444",
	}

	for _, pattern := range dangerousPatterns {
		t.Run(pattern, func(t *testing.T) {
			err := validateCommandSafety(pattern, nil, "")
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), "dangerous pattern")
		})
	}
}

func TestRunShellCommandTool_Denylist_ShellInjection(t *testing.T) {
	injectionPatterns := []string{
		"echo $(whoami)",
		"echo `whoami`",
		"cat /etc/passwd | grep root",
		"ls | grep test",
	}

	for _, pattern := range injectionPatterns {
		t.Run(pattern, func(t *testing.T) {
			err := validateCommandSafety(pattern, nil, "")
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), "shell injection")
		})
	}
}

func TestRunShellCommandTool_Denylist_CurlWgetAsCommandNameBlocked(t *testing.T) {
	for _, cmd := range []string{"curl", "wget"} {
		t.Run(cmd, func(t *testing.T) {
			err := validateCommandSafety(cmd, []string{"http://example.com"}, "")
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), "blocked by safety policy")
		})
	}
}

func TestRunShellCommandTool_Denylist_CurlWgetAsSubstringAllowed(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"slew", []string{"10.43.0.40:9000", "45.0", "30.0"}},
		{"echo", []string{"use curl to download"}},
		{"grep", []string{"curl", "file.txt"}},
		{"cat", []string{"wget-log.txt"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			err := validateCommandSafety(tc.command, tc.args, "")
			require.NoError(t, err)
		})
	}
}

func TestRunShellCommandTool_Denylist_AllowsSafeCommands(t *testing.T) {
	safeCommands := []struct {
		command string
		args    []string
	}{
		{"ls", []string{"-la"}},
		{"echo", []string{"hello"}},
		{"cat", []string{"/etc/hostname"}},
		{"grep", []string{"pattern", "file.txt"}},
		{"find", []string{".", "-name", "*.go"}},
		{"date", nil},
		{"uname", []string{"-a"}},
		{"ps", []string{"aux"}},
		{"df", []string{"-h"}},
	}

	for _, tc := range safeCommands {
		t.Run(tc.command, func(t *testing.T) {
			err := validateCommandSafety(tc.command, tc.args, "")
			require.NoError(t, err)
		})
	}
}

func TestRunShellCommandTool_CommandLineInCommandNamesArgvForm(t *testing.T) {
	err := validateCommandSafety("cat -n", []string{"/var/log/app.log"}, "")
	require.ErrorIs(t, err, constants.ErrMCPRunShellCommandNotExecutable)
	require.Contains(t, err.Error(), `{"args":["-n","/var/log/app.log"],"command":"cat"}`)
}

func TestValidateForSSHExecution_BlocksShellMetacharacters(t *testing.T) {
	tmpDir := "/tmp"
	if runtime.GOOS == "windows" {
		tmpDir = "C:\\tmp"
	}

	metacharacterTests := []struct {
		command     string
		args        []string
		workingDir  string
		expectError bool
	}{
		{"echo", []string{"test$(whoami)"}, "", true},                             // $ in args
		{"echo", []string{"test`whoami`"}, "", true},                              // backtick in args
		{"echo", []string{"test; rm -rf /"}, "", true},                            // semicolon in args
		{"echo", []string{"test& rm -rf /"}, "", true},                            // ampersand in args
		{"echo", []string{"test| rm -rf /"}, "", true},                            // pipe in args
		{"echo", []string{"test> /dev/null"}, "", true},                           // redirect in args
		{"echo", []string{"test< /etc/passwd"}, "", true},                         // redirect in args
		{"echo", []string{"test\nrm -rf /"}, "", true},                            // newline in args
		{"echo", []string{"test\r"}, "", true},                                    // carriage return in args
		{"echo", []string{"test\\n"}, "", true},                                   // backslash in args
		{"echo$(whoami)", []string{}, "", true},                                   // $ in command
		{"echo`whoami`", []string{}, "", true},                                    // backtick in command
		{"echo; rm", []string{}, "", true},                                        // semicolon in command
		{"echo", []string{"test"}, tmpDir + "/test\n", true},                      // newline in working dir
		{"echo", []string{"test"}, tmpDir + "/test;", true},                       // semicolon in working dir
		{"echo", []string{"test"}, tmpDir + "/test$", true},                       // $ in working dir
		{"echo", []string{"test"}, tmpDir + "/test`", true},                       // backtick in working dir
		{"echo", []string{"test"}, tmpDir + "/test\\", runtime.GOOS != "windows"}, // backslash in working dir (safe on Windows)
		{"echo", []string{"test"}, tmpDir + "/test|", true},                       // pipe in working dir
		{"echo", []string{"test"}, tmpDir + "/test>", true},                       // redirect in working dir
		{"echo", []string{"test"}, tmpDir + "/test<", true},                       // redirect in working dir
		{"echo", []string{"test"}, tmpDir + "/test&", true},                       // ampersand in working dir
		{"echo", []string{"test"}, tmpDir + "/test\r", true},                      // carriage return in working dir
		{"echo", []string{"test"}, "", false},                                     // safe: no metacharacters
		{"cat", []string{"/etc/hostname"}, "", false},                             // safe: normal command
	}

	for _, tc := range metacharacterTests {
		t.Run(tc.command+"_"+tc.workingDir, func(t *testing.T) {
			err := validateForSSHExecution(tc.command, tc.args, tc.workingDir)
			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "shell metacharacter")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateForSSHExecution_StillBlocksDenylist(t *testing.T) {
	// Ensure SSH validation still enforces the standard denylist
	dangerousCommands := []string{"rm", "sudo", "su", "dd"}

	for _, cmd := range dangerousCommands {
		t.Run(cmd, func(t *testing.T) {
			err := validateForSSHExecution(cmd, nil, "")
			require.Error(t, err)
		})
	}
}

func TestValidateForSSHExecution_AllowsSafeCommands(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	safeCommands := []struct {
		command    string
		args       []string
		workingDir string
	}{
		{"ls", []string{"-la"}, ""},
		{"echo", []string{"hello world"}, ""},
		{"cat", []string{"/etc/hostname"}, ""},
		{"grep", []string{"pattern", "file.txt"}, ""},
		{"find", []string{".", "-name", "*.go"}, ""},
		{"date", nil, ""},
		{"pwd", nil, tmpDir},
	}

	// Skip Linux-specific paths on Windows
	if runtime.GOOS != "windows" {
		safeCommands = append(safeCommands, struct {
			command    string
			args       []string
			workingDir string
		}{"ls", []string{}, "/var/log"})
	}

	for _, tc := range safeCommands {
		t.Run(tc.command, func(t *testing.T) {
			err := validateForSSHExecution(tc.command, tc.args, tc.workingDir)
			require.NoError(t, err)
		})
	}
}

func TestValidateCommandSafety_WorkingDirValidation(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	workingDirTests := []struct {
		name        string
		command     string
		args        []string
		workingDir  string
		expectError bool
		errorMsg    string
	}{
		{"valid absolute dir", "ls", []string{}, tmpDir, false, ""},
		{"path traversal", "ls", []string{}, tmpDir + "/../etc", true, "path traversal"},
		{"relative path", "ls", []string{}, "tmp", true, "absolute path"},
		{"empty working dir", "ls", []string{}, "", false, ""},
	}

	// Add Linux-specific tests only on non-Windows platforms
	if runtime.GOOS != "windows" {
		workingDirTests = append(workingDirTests,
			struct {
				name        string
				command     string
				args        []string
				workingDir  string
				expectError bool
				errorMsg    string
			}{"valid absolute dir with args", "pwd", nil, "/var/log", false, ""},
			struct {
				name        string
				command     string
				args        []string
				workingDir  string
				expectError bool
				errorMsg    string
			}{"nonexistent dir", "ls", []string{}, "/nonexistent_dir_12345", true, "does not exist"},
			struct {
				name        string
				command     string
				args        []string
				workingDir  string
				expectError bool
				errorMsg    string
			}{"file instead of dir", "ls", []string{}, "/etc/passwd", true, "not a directory"},
			struct {
				name        string
				command     string
				args        []string
				workingDir  string
				expectError bool
				errorMsg    string
			}{"complex valid path", "ls", []string{}, "/var/log", false, ""},
		)
	}

	for _, tc := range workingDirTests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommandSafety(tc.command, tc.args, tc.workingDir)
			if tc.expectError {
				require.Error(t, err)
				if tc.errorMsg != "" {
					require.Contains(t, err.Error(), tc.errorMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
