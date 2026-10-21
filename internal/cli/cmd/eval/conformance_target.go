// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// selectConformanceDataOperator considers every active Data session, including
// embedded. Capability preference must not hide choices from an interactive user.
func selectConformanceDataOperator(cmd *cobra.Command, operators []*operatorv1.OperatorDocument, operatorID string) (*operatorv1.OperatorDocument, error) {
	var matches []*operatorv1.OperatorDocument
	for _, op := range operators {
		if op == nil || !operatorcapability.IsDataOperator(op) || (operatorID != "" && op.GetId() != operatorID) {
			continue
		}
		matches = append(matches, op)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("evaluation: no active Data Operator matches %q; check ztd operator list: %w", operatorID, constants.ErrEvaluationTargetUnavailable)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	// An id pins a runtime, never an arbitrary one of its sessions.
	if operatorID != "" {
		return nil, fmt.Errorf("evaluation: Data Operator %s has %d active sessions: %w", operatorID, len(matches), constants.ErrEvaluationTargetAmbiguous)
	}
	sort.Slice(matches, func(i, j int) bool {
		left, right := conformanceOperatorLabel(matches[i]), conformanceOperatorLabel(matches[j])
		if left != right {
			return left < right
		}
		return matches[i].GetOperatorSessionId() < matches[j].GetOperatorSessionId()
	})
	if output.JSONEnabled(cmd) {
		return nil, fmt.Errorf("evaluation: %d Data Operators are available; run without --json to choose, or pass --operator-id for automation: %w", len(matches), constants.ErrEvaluationTargetAmbiguous)
	}
	out := cmd.ErrOrStderr()
	if _, err := fmt.Fprintln(out, "Choose a Data Operator:"); err != nil {
		return nil, err
	}
	for i, op := range matches {
		if _, err := fmt.Fprintf(out, "  %d. %s (session %s)\n", i+1, conformanceOperatorLabel(op), op.GetOperatorSessionId()); err != nil {
			return nil, err
		}
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	for {
		if err := cmd.Context().Err(); err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(out, "Selection [1-%d]: ", len(matches)); err != nil {
			return nil, err
		}
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("evaluation: read Data Operator selection: %w", readErr)
		}
		choice, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && choice >= 1 && choice <= len(matches) {
			return matches[choice-1], nil
		}
		if errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("evaluation: no Data Operator selected; run interactively to choose or pass --operator-id for automation: %w", constants.ErrEvaluationTargetAmbiguous)
		}
		if _, err := fmt.Fprintf(out, "Enter a number from 1 to %d.\n", len(matches)); err != nil {
			return nil, err
		}
	}
}

func conformanceOperatorLabel(op *operatorv1.OperatorDocument) string {
	if op.GetId() == string(constants.DocIDEmbeddedOperator) {
		return "Gateway (embedded Data Operator)"
	}
	name := strings.Join(strings.Fields(op.GetName()), " ")
	if name == "" {
		name = op.GetId()
	}
	if host := strings.Join(strings.Fields(op.GetCurrentHostname()), " "); host != "" {
		name += " on " + host
	}
	return name
}

// conformanceOperatorRoot uses the enrolled runtime directory, not the shell's
// current working directory. An explicit override supports locally mounted roots.
func conformanceOperatorRoot(override string, op *operatorv1.OperatorDocument) (string, error) {
	root := override
	if root == "" {
		root = op.GetLocalDir()
		if root == "" {
			root = op.GetRuntimeConfig().GetLocalDir()
		}
		if root == "" || !filepath.IsAbs(root) {
			return "", fmt.Errorf("evaluation: %s has no absolute runtime directory in its enrollment; provide its locally accessible root with --operator-root: %w", conformanceOperatorLabel(op), constants.ErrEvaluationTargetUnavailable)
		}
	}
	resolved, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("evaluation: resolve operator root: %w", err)
	}
	return resolved, nil
}

func writeConformanceSetup(out io.Writer, outboundDoc *operatorv1.OperatorDocument, unavailable map[cases.Ingress]evalv1.ConformanceObservationKind) error {
	label := "Gateway (embedded Data Operator)"
	if outboundDoc != nil {
		label = conformanceOperatorLabel(outboundDoc)
	}
	if _, err := fmt.Fprintf(out, "Data Operator: %s\n", label); err != nil {
		return err
	}
	if outboundDoc == nil {
		if _, err := fmt.Fprintln(out, "Not applicable: outbound dispatch and CLI coverage (embedded Data Operator selected)."); err != nil {
			return err
		}
	}
	if _, ok := unavailable[cases.IngressA2A]; ok {
		if _, err := fmt.Fprintln(out, "Unavailable: A2A coverage (the Gateway exposes A2A ingress; pass --a2a-stub-listen matching its --a2a-downstream-url to include it)."); err != nil {
			return err
		}
	}
	return nil
}
