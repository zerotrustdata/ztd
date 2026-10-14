// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestNewScenarioWorkspaceOperatorHostStyles(t *testing.T) {
	posix, err := NewScenarioWorkspace("/srv/ztd/", "run-1", "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, "/srv/ztd", posix.OperatorWorkingDirectory)
	assert.True(t, strings.HasPrefix(posix.Root, "/srv/ztd/"+constants.EvaluationWorkspaceDirname+"/"))

	windows, err := NewScenarioWorkspace(`d:\ztd\v1.0.6\ztd`, "run-1", "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, "D:/ztd/v1.0.6/ztd", windows.OperatorWorkingDirectory)
	assert.Equal(t, "D:/ztd/v1.0.6/ztd"+strings.TrimPrefix(posix.Root, "/srv/ztd"), windows.Root)
	require.NoError(t, windows.Validate("run-1", "attempt-1"))

	for _, invalid := range []string{"", "relative/dir", `relative\dir`, "D:", "D:relative", `\\server\share`} {
		_, err := NewScenarioWorkspace(invalid, "run-1", "attempt-1")
		require.ErrorIs(t, err, constants.ErrEvaluationWorkspaceUnavailable, invalid)
	}
}

func TestScenarioWorkspaceResolveWindowsOperator(t *testing.T) {
	ws, err := NewScenarioWorkspace(`D:\ztd\work`, "run-1", "attempt-1")
	require.NoError(t, err)
	file := ws.Root + "/notes.txt"

	for value, want := range map[string]string{
		file:                               file,
		strings.ReplaceAll(file, "/", `\`): file,
		"d" + file[1:]:                     file,
		`sub\a.txt`:                        "D:/ztd/work/sub/a.txt",
		"./sub/../b.txt":                   "D:/ztd/work/b.txt",
		`\outside\c.txt`:                   "D:/outside/c.txt",
		"E:/elsewhere":                     "E:/elsewhere",
		`D:\..\..\escape`:                  "D:/escape",
		`..\..\..\..\escape`:               "D:/escape",
	} {
		assert.Equal(t, want, ws.Resolve(value), value)
	}
	assert.True(t, ws.Contains(strings.ReplaceAll(file, "/", `\`)))
	assert.False(t, ws.Contains(`D:\ztd\work`))
	assert.False(t, ws.Contains(ws.Root+"-sibling/x"))
}

func TestScenarioWorkspaceResolvePOSIXOperator(t *testing.T) {
	ws, err := NewScenarioWorkspace("/srv/ztd", "run-1", "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, "/srv/ztd/sub/a.txt", ws.Resolve("sub/a.txt"))
	assert.Equal(t, "/etc/passwd", ws.Resolve("../../etc/passwd"))
	// A backslash is an ordinary file-name byte on a POSIX operator.
	assert.Equal(t, `/srv/ztd/sub\a.txt`, ws.Resolve(`sub\a.txt`))
	assert.True(t, ws.Contains(ws.Root+"/x"))
}

func TestValidateAgentDataOperator(t *testing.T) {
	cases := []struct {
		name     string
		embedded bool
		wd       string
		project  string
		ok       bool
	}{
		{name: "dedicated sandbox outside the project", wd: "/srv/eval-sandbox", project: "/srv/ztd", ok: true},
		{name: "embedded operator", embedded: true, wd: "/srv/eval-sandbox", project: "/srv/ztd"},
		{name: "project root", wd: "/srv/ztd/", project: "/srv/ztd"},
		{name: "inside the project", wd: "/srv/ztd/sandbox", project: "/srv/ztd"},
		{name: "ancestor of the project", wd: "/srv", project: "/srv/ztd"},
		{name: "relative working directory", wd: "sandbox", project: "/srv/ztd"},
		{name: "relative project root", wd: "/srv/eval-sandbox", project: "ztd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgentDataOperator(tc.embedded, tc.wd, tc.project)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, constants.ErrEvaluationWorkspaceUnavailable)
		})
	}
}

func TestScenarioWorkspaceSentinelPathSitsBesideRootOutsideTheWorkspace(t *testing.T) {
	posix, err := NewScenarioWorkspace("/srv/ztd", "run-1", "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, path.Dir(posix.Root)+"/"+path.Base(posix.Root)+constants.EvaluationWorkspaceSentinelSuffix, posix.SentinelPath(),
		"the sentinel is a sibling named after the workspace root")
	assert.False(t, strings.HasPrefix(posix.SentinelPath(), posix.Root+"/"), "the sentinel sits outside the model's workspace")

	windows, err := NewScenarioWorkspace(`d:\ztd\v1.0.6\ztd`, "run-1", "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, path.Dir(windows.Root)+"/"+path.Base(windows.Root)+constants.EvaluationWorkspaceSentinelSuffix, windows.SentinelPath())
	assert.True(t, strings.HasPrefix(windows.SentinelPath(), "D:/ztd/v1.0.6/ztd/"), "a Windows operator keeps the slash-form drive path")
}
