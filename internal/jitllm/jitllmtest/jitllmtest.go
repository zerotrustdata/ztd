// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package jitllmtest builds real jitllm containers for tests from the
// committed stories260K GGUF, a 260K-parameter llama, so the in-process
// engine runs actual weights on the host without a download.
package jitllmtest

import (
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"github.com/jitllm/jitllm/convert"
	"github.com/jitllm/jitllm/format/jlm"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// stories260K is ggml-org's tinyllamas/stories260K.gguf, the model jitllm
// commits for its own hermetic tests.
//
//go:embed testdata/stories260K.gguf
var stories260K []byte

// chatTemplate is ChatML. stories260K carries no template of its own, and
// the Inference Operator only sends chat prompts.
const chatTemplate = "{% for message in messages %}<|im_start|>{{ message.role }}\n{{ message.content }}<|im_end|>\n{% endfor %}" +
	"{% if add_generation_prompt %}<|im_start|>assistant\n{% endif %}"

// WriteChatContainer converts stories260K with a ChatML template into
// dir/name.jlm and returns the container path. scratch holds the source
// GGUF and template and must not be dir.
func WriteChatContainer(tb testing.TB, scratch, dir, name string) string {
	tb.Helper()
	src := filepath.Join(scratch, "stories260K.gguf")
	if err := os.WriteFile(src, stories260K, constants.PermFilePrivate); err != nil {
		tb.Fatalf("jitllmtest: write source model: %v", err)
	}
	tmpl := filepath.Join(scratch, "chatml.jinja")
	if err := os.WriteFile(tmpl, []byte(chatTemplate), constants.PermFilePrivate); err != nil {
		tb.Fatalf("jitllmtest: write chat template: %v", err)
	}
	dst := filepath.Join(dir, name+".jlm")
	if _, err := convert.FromGGUF(src, dst, jlm.Fingerprint{}, convert.WithChatTemplate(tmpl)); err != nil {
		tb.Fatalf("jitllmtest: convert stories260K: %v", err)
	}
	return dst
}
