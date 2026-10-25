// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/require"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestConsensusStatusLine(t *testing.T) {
	bound := models.HealthResponse{
		Posture:   constants.PostureNotary,
		Consensus: models.HealthConsensus{Bound: true, Provider: "tribunal", Quorum: 3, Members: 5, Usable: 5, Ready: true},
	}
	require.Equal(t, "Consensus  tribunal 3-of-5 | signers 5/5", consensusStatusLine(bound))

	unbound := models.HealthResponse{Posture: constants.PostureNotary}
	require.Equal(t, "Consensus  NOT BOUND — posture notary requires L2: ztd consensus enroll tribunal --model <tag>", consensusStatusLine(unbound))
}

func TestGatewayStatusDistinguishesUnknownAndEmptyRegistry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operators []byte
		want      string
	}{
		{"unavailable", nil, "Operators  unavailable (registry query failed)"},
		{"malformed", []byte("not JSON"), "Operators  unavailable (registry query failed)"},
		{"rejected", []byte(`{"success":false}`), "Operators  unavailable (registry query failed)"},
		{"empty", []byte(`{"success":true,"operators":[]}`), "Operators  none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			mock := &statusMockClient{responses: map[string][]byte{"/api/v1/health": []byte(`{"status":"ok"}`)}}
			if tc.operators != nil {
				mock.responses[constants.APIPaths.Operators] = tc.operators
			}
			factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mock, nil }
			cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), factory, cmdtest.FileSvcFactoryFor(fileSvc))
			cmd.SetArgs(nil)
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			require.NoError(t, cmd.Execute())
			require.Contains(t, buf.String(), "Gateway  online")
			require.Contains(t, buf.String(), tc.want)
			require.NotContains(t, buf.String(), "Endpoints:")
		})
	}
}

func TestGatewayStatusOnlyShowsConnectedOperators(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{
		{OperatorCapabilities: []string{string(constants.OperatorCapabilityInference)}, CurrentHostname: "gpu-host", Status: string(constants.OperatorStatusActive)},
		{CurrentHostname: "stopped-host", Status: string(constants.OperatorStatusStopped)},
		{CurrentHostname: "unclaimed-host", Status: string(constants.OperatorStatusActive), IsSlot: true},
	}})
	require.NoError(t, err)
	mock := &statusMockClient{responses: map[string][]byte{"/api/v1/health": []byte(`{"status":"ok"}`), constants.APIPaths.Operators: body}}
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mock, nil }
	cmd, buf := newOutputCmd()
	require.NoError(t, printGatewayStatus(cmd, cfg, factory, cmdtest.FileSvcFactoryFor(fileSvc)))
	require.Contains(t, buf.String(), "CAPABILITIES")
	require.Contains(t, buf.String(), "inference")
	require.Contains(t, buf.String(), "gpu-host")
	require.NotContains(t, buf.String(), "stopped-host")
	require.NotContains(t, buf.String(), "unclaimed-host")
}

func TestQuietStartKeepsRunningGatewayAndSuppressesGuidance(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	pidPath := filepath.Join(constants.PidDirname, constants.OperatorPIDFilename)
	pid := []byte(strconv.Itoa(os.Getpid()))
	require.NoError(t, fileSvc.WriteFile(context.Background(), pidPath, pid, constants.PermFilePrivate))
	cmd := gatewayStartCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc), nil)
	cmd.SetArgs([]string{"--quiet", "--cert-mode", "localhost"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Execute())
	require.Empty(t, buf.String())
	saved, err := fileSvc.ReadFile(context.Background(), pidPath)
	require.NoError(t, err)
	require.Equal(t, pid, saved)
	cmd.Println("output restored")
	require.Contains(t, buf.String(), "output restored")
}

func TestStatusShowsLocalEmbeddedCapabilitiesWithoutCLIEnrollment(t *testing.T) {
	for _, capabilities := range []constants.OperatorCapabilities{
		{},
		{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver},
	} {
		t.Run(capabilities.String(), func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorPIDFilename), []byte(strconv.Itoa(os.Getpid())), constants.PermFilePrivate))
			require.NoError(t, serve.WriteLaunchProfile(fileSvc, serve.GatewayConfig{OperatorCapabilities: capabilities, Posture: "doctrine", LogLevel: constants.LogLevelInfo}))
			expected := capabilities.WithDefault().String()
			var full bytes.Buffer
			printOperatorTables(&full, collectOperatorInventory(nil, fileSvc, cfg))
			require.Contains(t, full.String(), string(constants.DocIDEmbeddedOperator))
			require.Contains(t, full.String(), expected)
			require.Contains(t, full.String(), "Remote Operators  unavailable (CLI not enrolled)")
			require.NotContains(t, full.String(), "No connected operators")
			factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
				return nil, constants.ErrNotFound
			}
			cmd, status := newOutputCmd()
			require.NoError(t, printGatewayStatus(cmd, cfg, factory, cmdtest.FileSvcFactoryFor(fileSvc)))
			require.Contains(t, status.String(), string(constants.DocIDEmbeddedOperator))
			require.Contains(t, status.String(), expected)
		})
	}
}

func TestLocalEmbeddedOperatorOmitsStoppedRuntimeAndMarksInvalidProfileUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		valid   bool
		want    bool
	}{
		{"stopped", false, true, false}, {"missing profile", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			if tc.running {
				require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorPIDFilename), []byte(strconv.Itoa(os.Getpid())), constants.PermFilePrivate))
			}
			if tc.valid {
				require.NoError(t, serve.WriteLaunchProfile(fileSvc, serve.GatewayConfig{OperatorCapabilities: constants.OperatorCapabilities{constants.OperatorCapabilityInference}, Posture: "doctrine", LogLevel: constants.LogLevelInfo}))
			}
			var buf bytes.Buffer
			inv := collectOperatorInventory(nil, fileSvc, cfg)
			require.Equal(t, tc.want, len(inv.Operators) == 1)
			printOperatorTables(&buf, inv)
			require.NotContains(t, buf.String(), "inference")
			if tc.want {
				require.Equal(t, []string{string(constants.DocIDEmbeddedOperator), "embedded", "running", "local", "-", "unknown", filepath.Join(fileSvc.Resolve(""), constants.OperatorsDirname, constants.EmbeddedOperatorDirname)}, tableRow(t, buf.String(), string(constants.DocIDEmbeddedOperator)))
			}
		})
	}
}

func TestStatusDoesNotDuplicateEnrolledEmbeddedOperator(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorPIDFilename), []byte(strconv.Itoa(os.Getpid())), constants.PermFilePrivate))
	require.NoError(t, serve.WriteLaunchProfile(fileSvc, serve.GatewayConfig{OperatorCapabilities: constants.OperatorCapabilities{constants.OperatorCapabilityInference}, Posture: "doctrine", LogLevel: constants.LogLevelInfo}))
	body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{
		{Id: string(constants.DocIDEmbeddedOperator), OperatorType: string(constants.OperatorTypeEmbedded), Status: string(constants.OperatorStatusActive), OperatorCapabilities: []string{string(constants.OperatorCapabilityInference)}},
	}})
	require.NoError(t, err)
	mock := &statusMockClient{responses: map[string][]byte{constants.APIPaths.Operators: body}}
	var buf bytes.Buffer
	printOperatorTables(&buf, collectOperatorInventory(mock, fileSvc, cfg))
	require.Contains(t, buf.String(), "inference")
	require.Equal(t, 1, operatorTableRows(buf.String(), string(constants.DocIDEmbeddedOperator)))
	require.NotContains(t, buf.String(), "launch profile")
}

func TestGatewayStatusShowsLocalOperatorTablesWithoutEnrollment(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorPIDFilename), []byte(strconv.Itoa(os.Getpid())), constants.PermFilePrivate))
	require.NoError(t, serve.WriteLaunchProfile(fileSvc, serve.GatewayConfig{OperatorCapabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver}, Posture: "doctrine", LogLevel: constants.LogLevelInfo}))
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return nil, constants.ErrNotFound
	}
	cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), factory, cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs(nil)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Execute())
	require.Contains(t, buf.String(), "embedded-operator")
	require.Contains(t, buf.String(), "data,inference,provenance,observer")
	require.Contains(t, buf.String(), "CLI not enrolled")
	require.Contains(t, buf.String(), "Remote Operators  unavailable (CLI not enrolled)")
	require.Contains(t, buf.String(), "Enrollments  unavailable (CLI not enrolled)")
	require.Equal(t, 1, strings.Count(buf.String(), "Enrollments"))
	require.NotContains(t, buf.String(), "launch profile")
	require.NotContains(t, buf.String(), "check enrollment or Gateway logs")
}

func TestGatewayStatusHasOneOutputFormat(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return nil, constants.ErrNotFound
	}
	cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), factory, cmdtest.FileSvcFactoryFor(fileSvc))
	require.Nil(t, cmd.Flags().Lookup("brief"))
	require.Nil(t, cmd.Flags().Lookup("details"))
}

// tableRow returns the whitespace-separated cells of the first row of out that
// starts with id. Test values contain no spaces.
func tableRow(t *testing.T, out, id string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == id {
			return fields
		}
	}
	t.Fatalf("no row for %q in:\n%s", id, out)
	return nil
}

// operatorTableRows counts the Operators-table rows for id, excluding the
// Operator flags table.
func operatorTableRows(out, id string) int {
	table, _, _ := strings.Cut(out, "Operator flags")
	count := 0
	for _, line := range strings.Split(table, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == id {
			count++
		}
	}
	return count
}

func lineStartingWith(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// flagRowsFor returns the (capability, flag, value) rows listed for id.
func flagRowsFor(out, id string) [][]string {
	var rows [][]string
	inFlags := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "Operator flags":
			inFlags = true
		case inFlags && strings.HasPrefix(strings.TrimSpace(line), id+" "):
			rows = append(rows, strings.Fields(line)[1:])
		}
	}
	return rows
}

func TestGatewayStatusListsEveryOperatorWithTypeCapabilitiesAndStartValues(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, serve.WriteLaunchProfile(fileSvc, serve.GatewayConfig{
		OperatorCapabilities:    constants.OperatorCapabilities{constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance},
		InferenceOllamaEndpoint: "http://192.168.1.2:11434",
		InferenceKeepAlive:      "30m",
		Posture:                 "doctrine",
		LogLevel:                constants.LogLevelInfo,
	}))
	body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{
		{
			Id: "op-prov", OperatorType: string(constants.OperatorTypeRemote), Status: string(constants.OperatorStatusActive), CurrentHostname: "store-host",
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:                       []string{string(constants.OperatorCapabilityProvenance)},
				ProvenanceOperatorModelStorageRoot: "/srv/models", LocalDir: "/srv/ztd/provenance", HttpPort: 8080,
				// Reported by every Operator, but only meaningful with the inference capability.
				InferenceOllamaEndpoint: "http://ignored:11434",
			},
		},
		{
			Id: "op-inf", OperatorType: string(constants.OperatorTypeRemote), Status: string(constants.OperatorStatusStale), CurrentHostname: "gpu-host",
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:            []string{string(constants.OperatorCapabilityInference)},
				InferenceOllamaEndpoint: "http://gpu-host:11434", LocalDir: "/srv/ztd/inference", HttpPort: 8080,
				LogLevel: "debug",
			},
		},
		{
			// One remote Operator holding two capabilities shows both, with each capability's reported flags.
			Id: "op-witness", OperatorType: string(constants.OperatorTypeRemote), Status: string(constants.OperatorStatusActive), CurrentHostname: "beepboop",
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:                       []string{string(constants.OperatorCapabilityObserver), string(constants.OperatorCapabilityProvenance)},
				ProvenanceOperatorModelStorageRoot: `D:\models`, ProvenanceOperatorId: "beepboop-provenance",
				ProviderBoundaryObserverId: "beepboop-observer", LocalDir: `D:\models`, HttpPort: 8080,
			},
		},
		{
			Id: string(constants.DocIDEmbeddedOperator), OperatorType: string(constants.OperatorTypeEmbedded), Status: string(constants.OperatorStatusActive),
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:                       []string{string(constants.OperatorCapabilityInference), string(constants.OperatorCapabilityProvenance)},
				InferenceOllamaEndpoint:            "http://192.168.1.2:11434",
				ProvenanceOperatorModelStorageRoot: "/mnt/models", LocalDir: "/home/me/ztd", HttpPort: 8081,
			},
		},
	}})
	require.NoError(t, err)
	mock := &statusMockClient{responses: map[string][]byte{"/api/v1/health": []byte(`{"status":"ok"}`), constants.APIPaths.Operators: body}}
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mock, nil }
	cmd, buf := newOutputCmd()
	require.NoError(t, printGatewayStatus(cmd, cfg, factory, cmdtest.FileSvcFactoryFor(fileSvc)))
	out := buf.String()

	require.Contains(t, out, "\n  OPERATOR ID ")
	require.Equal(t, []string{"OPERATOR", "ID", "TYPE", "STATUS", "HOST", "HTTP", "PORT", "CAPABILITIES", "DIRECTORY"}, strings.Fields(lineStartingWith(out, "  OPERATOR ID")))
	require.Equal(t, []string{"embedded-operator", "embedded", "active", "local", "8081", "inference,provenance", "/home/me/ztd"}, tableRow(t, out, "embedded-operator"))
	require.Equal(t, []string{"op-inf", "remote", "stale", "gpu-host", "8080", "inference", "/srv/ztd/inference"}, tableRow(t, out, "op-inf"))
	require.Equal(t, []string{"op-prov", "remote", "active", "store-host", "8080", "provenance", "/srv/ztd/provenance"}, tableRow(t, out, "op-prov"))
	require.Less(t, strings.Index(out, "embedded-operator"), strings.Index(out, "op-inf"), "embedded Operator is listed first, then by ID")

	// The embedded Operator's own launch profile adds values the registry lacks.
	require.Equal(t, [][]string{
		{"inference", "--inference-ollama-endpoint", "http://192.168.1.2:11434"},
		{"inference", "--inference-keep-alive", "30m"},
		{"provenance", "--model-storage-root", "/mnt/models"},
		{"provenance", "--provenance-operator-id", "(default)"},
	}, flagRowsFor(out, "embedded-operator"))
	// A remote Operator lists only what it reported: common flags only when customised,
	// capability flags only for held capabilities, and nothing it never sent the Gateway.
	require.Equal(t, [][]string{
		{"all", "--log", "debug"},
		{"inference", "--inference-ollama-endpoint", "http://gpu-host:11434"},
	}, flagRowsFor(out, "op-inf"))
	require.Equal(t, [][]string{
		{"provenance", "--model-storage-root", "/srv/models"},
	}, flagRowsFor(out, "op-prov"))
	require.Equal(t, []string{"op-witness", "remote", "active", "beepboop", "8080", "provenance,observer", `D:\models`}, tableRow(t, out, "op-witness"))
	require.Equal(t, [][]string{
		{"provenance", "--model-storage-root", `D:\models`},
		{"provenance", "--provenance-operator-id", "beepboop-provenance"},
		{"observer", "--provider-boundary-observer-id", "beepboop-observer"},
	}, flagRowsFor(out, "op-witness"))
	require.NotContains(t, out, "http://ignored:11434")
	require.NotContains(t, out, "Remote Operators  unavailable")
}

func TestGatewayStatusLocalFlagsComeFromLaunchProfileWithoutEnrollment(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorPIDFilename), []byte(strconv.Itoa(os.Getpid())), constants.PermFilePrivate))
	require.NoError(t, serve.WriteLaunchProfile(fileSvc, serve.GatewayConfig{
		OperatorCapabilities:               constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver},
		ProvenanceOperatorModelStorageRoot: "/mnt/d/ai/Ollama/models",
		ProviderBoundaryObserverID:         "ztd-observer",
		HTTPPort:                           8080,
		Posture:                            "doctrine",
		LogLevel:                           constants.LogLevelInfo,
	}))
	var buf bytes.Buffer
	printOperatorTables(&buf, collectOperatorInventory(nil, fileSvc, cfg))
	require.Equal(t, []string{"embedded-operator", "embedded", "running", "local", "8080", "data,provenance,observer", filepath.Join(fileSvc.Resolve(""), constants.OperatorsDirname, constants.EmbeddedOperatorDirname)}, tableRow(t, buf.String(), "embedded-operator"))
	require.Equal(t, [][]string{
		{"provenance", "--model-storage-root", "/mnt/d/ai/Ollama/models"},
		{"provenance", "--provenance-operator-id", "(default)"},
		{"observer", "--provider-boundary-observer-id", "ztd-observer"},
	}, flagRowsFor(buf.String(), "embedded-operator"))
}

func TestGatewayStatusOmitsFlagsTableWhenNoOperatorHasFlags(t *testing.T) {
	var buf bytes.Buffer
	printOperatorTables(&buf, operatorInventory{Operators: []operatorStatus{{ID: "op-data", Type: "remote", Status: "active", Host: "h", Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData}}}})
	require.Contains(t, buf.String(), "op-data")
	require.NotContains(t, buf.String(), "Operator flags")
}
