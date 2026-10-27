// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

const testMountInfo = "100 90 0:50 / /mnt/d rw,noatime - 9p drvfs rw,aname=drvfs\n" +
	"101 90 8:1 / / rw - ext4 /dev/sdc rw\n"

func TestOsReleaseIsWSL(t *testing.T) {
	assert.True(t, osReleaseIsWSL("6.6.114.1-microsoft-standard-WSL2\n"))
	assert.False(t, osReleaseIsWSL("6.8.0-45-generic\n"))
	assert.False(t, osReleaseIsWSL(""))
}

func TestOnWindowsMount(t *testing.T) {
	assert.True(t, onWindowsMount(testMountInfo, "/mnt/d/ai/ollama/models"))
	assert.True(t, onWindowsMount(testMountInfo, "/mnt/d"))
	assert.False(t, onWindowsMount(testMountInfo, "/mnt/dx/models"))
	assert.False(t, onWindowsMount(testMountInfo, "/home/user/models"))
}

func TestWSLOllamaAdvisories(t *testing.T) {
	up := func(context.Context, string) bool { return true }
	down := func(context.Context, string) bool { return false }
	ctx := context.Background()

	assert.Empty(t, wslOllamaAdvisories(ctx, OllamaAdvisoryInput{}, up, testMountInfo))
	assert.Len(t, wslOllamaAdvisories(ctx, OllamaAdvisoryInput{}, down, testMountInfo), 1)
	assert.Len(t, wslOllamaAdvisories(ctx, OllamaAdvisoryInput{Endpoint: "http://172.29.176.1:11434"}, up, testMountInfo), 1)
	assert.Len(t, wslOllamaAdvisories(ctx, OllamaAdvisoryInput{ModelStorageRoot: "/mnt/d/ai/ollama/models"}, up, testMountInfo), 1)
}

func TestOllamaAdvisoriesNoopOutsideWSL(t *testing.T) {
	assert.Nil(t, Environment{}.OllamaAdvisories(context.Background(), OllamaAdvisoryInput{}))
}
