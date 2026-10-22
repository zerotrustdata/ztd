// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package scrubbing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newTestService builds a scrubbing service backed by an in-memory token store.
func newTestService(t *testing.T, config *Config) *ScrubbingService {
	t.Helper()
	service, err := NewScrubbingService(context.Background(), config, testutil.NewTestLogger(), storagetest.NewTestTokenStore())
	require.NoError(t, err)
	return service
}

// assertTokenized scrubs input and asserts that every secret was replaced by a
// reversible UEI token and that rehydration restores the input exactly.
func assertTokenized(t *testing.T, service *ScrubbingService, input string, secrets ...string) string {
	t.Helper()
	ctx := context.Background()
	result := service.ScrubText(ctx, input)
	assert.Contains(t, result, "{{UEI_", "input: %s", input)
	assert.NotContains(t, result, untokenizedValue, "input: %s", input)
	for _, secret := range secrets {
		assert.NotContains(t, result, secret, "input: %s", input)
	}
	assert.Equal(t, input, mustRehydrateText(t, service, result), "rehydration must restore input: %s", input)
	return result
}

// mustRehydrateText rehydrates text and fails the test when any placeholder does not resolve.
func mustRehydrateText(t *testing.T, service *ScrubbingService, text string) string {
	t.Helper()
	rehydrated, err := service.RehydrateText(context.Background(), text, ForeignTokenReject)
	require.NoError(t, err)
	return rehydrated
}

// unmintedToken returns a token in service's own namespace that it never minted.
func unmintedToken(service *ScrubbingService) string {
	return fmt.Sprintf("{{UEI_%s_999999}}", service.tokenOwner)
}

// assertPreserved asserts that input passes through scrubbing unchanged.
func assertPreserved(t *testing.T, service *ScrubbingService, inputs ...string) {
	t.Helper()
	for _, input := range inputs {
		assert.Equal(t, input, service.ScrubText(context.Background(), input))
	}
}

func lenientConfig() *Config {
	return &Config{Enabled: true, StrictMode: false, RequirePersistence: true}
}

// failingTokenStore persists the owner namespace but fails every token write.
type failingTokenStore struct{ *storagetest.TestTokenStore }

func (f failingTokenStore) KVSet(ctx context.Context, key, value string, ttlSeconds int) error {
	if strings.HasPrefix(key, constants.ScrubbingTokenKeyPrefix) {
		return fmt.Errorf("kv set: %w", constants.ErrScrubbingTokenPersist)
	}
	return f.TestTokenStore.KVSet(ctx, key, value, ttlSeconds)
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()

	assert.True(t, config.Enabled, "should be enabled by default for safety")
	assert.True(t, config.StrictMode, "strict mode should be on by default")
	assert.True(t, config.RequirePersistence, "tokens must be persisted for rehydration")
	assert.Empty(t, config.AllowedPatterns)
	assert.Empty(t, config.CustomScrubPatterns)
}

func TestContainsToken(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "plain text token", payload: "cat {{UEI_3f9a0c12be47_1}}", want: true},
		{name: "ownerless token", payload: "cat {{UEI_1}}", want: true},
		{name: "JSON value token", payload: `{"path":"{{UEI_3f9a0c12be47_2}}"}`, want: true},
		{name: "JSON-escaped value token", payload: `{"path":"{{UEI_3f9a0c12be47_3}}"}`, want: true},
		{name: "JSON-escaped key token", payload: `{"{{UEI_3f9a0c12be47_4}}":1}`, want: true},
		{name: "JSON without token", payload: `{"path":"/tmp/x","n":[1,2]}`, want: false},
		{name: "text without token", payload: "ls -la", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ContainsToken([]byte(tt.payload)))
		})
	}
}

func TestNewScrubbingService(t *testing.T) {
	t.Parallel()

	t.Run("nil config uses defaults", func(t *testing.T) {
		t.Parallel()
		service := newTestService(t, nil)
		assert.True(t, service.config.StrictMode)
		assert.Greater(t, len(service.scrubbers), 10)
	})

	t.Run("required persistence without a store fails construction", func(t *testing.T) {
		t.Parallel()
		_, err := NewScrubbingService(context.Background(), DefaultConfig(), testutil.NewTestLogger(), nil)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenStoreUnavailable)
	})

	t.Run("disabled scrubbing needs no store", func(t *testing.T) {
		t.Parallel()
		service, err := NewScrubbingService(context.Background(), &Config{Enabled: false, RequirePersistence: true}, testutil.NewTestLogger(), nil)
		require.NoError(t, err)
		assert.False(t, service.IsEnabled())
	})
}

// Operational identifiers stay readable in both modes: the model needs them to troubleshoot.
func TestScrubbingService_ScrubText_OperationalDataPreserved(t *testing.T) {
	t.Parallel()
	for name, config := range map[string]*Config{"lenient": lenientConfig(), "default": DefaultConfig()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service := newTestService(t, config)
			assertPreserved(t, service,
				"Server at 192.168.1.1",
				"Connect to 10.0.0.1:5432",
				"IPs: 172.16.0.1 and 8.8.8.8",
				"ID: 550e8400-e29b-41d4-a716-446655440000",
				"User 123e4567-e89b-12d3-a456-426614174000 created",
				"File at /home/user/secret.txt",
				"Reading /etc/passwd",
				"Log: /var/log/app/debug.log",
				"arn:aws:s3:::my-bucket/path",
				"arn:aws:iam::123456789012:role/MyRole",
				"Account 123456789012 created",
				"see https://example.com/docs at 08:00:01",
				"Version: 24.0.7\nClient: Container Engine",
			)
		})
	}
}

// A log line whose fields are not sensitive passes through whole: no field is
// dropped or replaced (regression for the recovery-error-guided-retry eval).
func TestScrubbingService_ScrubText_LogFieldsPreserved(t *testing.T) {
	t.Parallel()
	service := newTestService(t, DefaultConfig())
	assertPreserved(t, service,
		"2026-09-16T08:00:01Z AUTH_FAILURE user=svc-deploy",
		"2026-09-16T08:00:03Z AUTH_FAILURE user=svc-web reason=expired",
		`{"user": "admin", "status": "ok"}`,
	)
}

// Scrubbing never cuts content: an output longer than the former 4096-byte budget is returned whole.
func TestScrubbingService_ScrubText_DoesNotTruncate(t *testing.T) {
	t.Parallel()
	service := newTestService(t, DefaultConfig())

	input := strings.Repeat("plain output line\n", 1000) + "tail"
	result := service.ScrubText(context.Background(), input)
	assert.Equal(t, input, result)
	assert.NotContains(t, result, "TRUNCATED")
}

func TestScrubbingService_ScrubText_Tokenizes(t *testing.T) {
	t.Parallel()
	service := newTestService(t, lenientConfig())

	tests := []struct {
		name    string
		input   string
		secrets []string
	}{
		{"email", "Contact admin@example.com", []string{"admin@example.com"}},
		{"plus-addressed email", "Email: user.name+tag@domain.org", []string{"user.name+tag@domain.org"}},
		{"password", "password=secretvalue123", []string{"secretvalue123"}},
		{"slack api key", "api_key: xoxb-123456789012-1234567890123-abcdef", []string{"xoxb-"}},
		{"token", "token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"}},
		{"ssn", "SSN: 123-45-6789", []string{"123-45-6789"}},
		{"credit card", "Card: 4111-1111-1111-1111", []string{"4111-1111-1111-1111"}},
		{"phone", "Call 555-123-4567", []string{"555-123-4567"}},
		{"mysql", "mysql://user:pass@host:3306/db", []string{"user:pass"}},
		{"postgres", "postgres://admin:secret@localhost/mydb", []string{"admin:secret"}},
		{"redis", "redis://default:password@redis.io:6379", []string{"default:password"}},
		{"url credentials", "fetch https://bob:hunter2@example.com/repo", []string{"bob:hunter2"}},
		{"aws access key", "Key: AKIAIOSFODNN7EXAMPLE", []string{"AKIAIOSFODNN7EXAMPLE"}},
		{"aws session key", "ASIA1234567890ABCDEF", []string{"ASIA1234567890ABCDEF"}},
		{"aws env key", "export AWS_ACCESS_KEY_ID=AKIA1234567890ABCDEF", []string{"AKIA1234567890ABCDEF"}},
		{"gcp api key", "gcp_key=AIzaSyDaGmWKa4JsXZ-HjGw7ISLn_3namBGewQe", []string{"AIzaSy"}},
		{"azure secret", `azure_client_secret="abc123def456ghi789jkl012mno345pqr678"`, []string{"abc123def456ghi789jkl012mno345pqr678"}},
		{"github token", "ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx1234", []string{"ghp_"}},
		{"slack bot token", "xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx", []string{"xoxb-"}},
		{"slack app token", "xapp-1-A1B2C3D4E5F-1234567890123-abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab", []string{"xapp-"}},
		{"okta token", "00abcDefGhIjKlMnOpQrStUvWxYz0123456789ABCD", []string{"00abcDef"}},
		{"azure ad secret", "abc8Q~defghijklmnopqrstuvwxyz1234567890AB", []string{"abc8Q~"}},
		{"sendgrid key", "SG.xxxxxxxxxxxxxxxxxxxxxx.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", []string{"SG.xxx"}},
		{"twilio sid", "Account SID: AC12345678901234567890123456789012", []string{"AC1234567890"}},
		{"npm token", "Using npm_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx for auth", []string{"npm_"}},
		{"german iban", "Bank account: DE89370400440532013000", []string{"DE89370400440532013000"}},
		{"uk iban", "Bank account: GB82WEST12345698765432", []string{"GB82WEST12345698765432"}},
		{"french iban", "Bank account: FR7630006000011234567890189", []string{"FR7630006000011234567890189"}},
		{"bearer header", "Authorization: Bearer abc123def456", []string{"abc123def456"}},
		{"bearer lowercase", "bearer some_random_token_value", []string{"some_random_token_value"}},
		{"bearer uppercase", "BEARER MySecretTokenHere", []string{"MySecretTokenHere"}},
		{"oauth secret", "client_secret=abcdefghijklmnopqrstuvwx", []string{"abcdefghijklmnopqrstuvwx"}},
		{"oauth secret colon", "oauth_secret: 12345678901234567890abcd", []string{"12345678901234567890abcd"}},
		{"oauth secret quoted", "clientSecret='mysupersecretclientkey'", []string{"mysupersecretclientkey"}},
		{
			"jwt",
			"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			[]string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		},
		{
			"private key",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy...\n-----END RSA PRIVATE KEY-----",
			[]string{"MIIEpAIBAAKCAQEA"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertTokenized(t, service, tt.input, tt.secrets...)
		})
	}
}

// The key of a key-value pair stays visible; only the value becomes a token.
func TestScrubbingService_ScrubText_KeepsKeysVisible(t *testing.T) {
	t.Parallel()
	service := newTestService(t, lenientConfig())

	assert.Regexp(t, `^password=\{\{UEI_[0-9a-f]{12}_\d+\}\}$`, assertTokenized(t, service, "password=secretvalue123", "secretvalue123"))
	assert.Regexp(t, `^Contact \{\{UEI_[0-9a-f]{12}_\d+\}\}$`, assertTokenized(t, service, "Contact admin@example.com", "admin@example.com"))
	assert.Regexp(t, `^fetch https://\{\{UEI_[0-9a-f]{12}_\d+\}\}@example\.com/repo$`, assertTokenized(t, service, "fetch https://bob:hunter2@example.com/repo", "hunter2"))
}

// Overlapping scrubbers never wrap one token inside another, so rehydration is exact.
func TestScrubbingService_ScrubText_OverlappingScrubbersRoundTrip(t *testing.T) {
	t.Parallel()
	service := newTestService(t, DefaultConfig())

	inputs := []string{
		"Authorization: Bearer abc123def456",
		"password=AKIAIOSFODNN7EXAMPLE",
		"api_key: xoxb-123456789012-1234567890123-abcdef",
		`secret="user@example.com"`,
	}
	for _, input := range inputs {
		result := assertTokenized(t, service, input)
		assert.NotRegexp(t, `\{\{UEI_[^}]*\{\{`, result, "nested token for input: %s", input)
	}
}

// Scrubbing scrubbed text changes nothing.
func TestScrubbingService_ScrubText_Idempotent(t *testing.T) {
	t.Parallel()
	service := newTestService(t, DefaultConfig())
	ctx := context.Background()

	inputs := []string{
		"Authorization: Bearer abc123def456",
		"password=secretvalue123 user=svc-deploy",
		`{"password":"hunter2","user":"svc-deploy"}`,
		"salary_info: 75000",
		"Contact admin@example.com",
	}
	for _, input := range inputs {
		once := service.ScrubText(ctx, input)
		assert.Equal(t, once, service.ScrubText(ctx, once), "input: %s", input)
	}
}

func TestScrubbingService_ScrubText_Disabled(t *testing.T) {
	t.Parallel()
	service := newTestService(t, &Config{Enabled: false})
	assert.Equal(t, suppressedOutput, service.ScrubText(context.Background(), "Sensitive data: 192.168.1.1"))
}

// A value whose token cannot be persisted never passes through in plaintext.
func TestScrubbingService_ScrubText_PersistFailureFailsClosed(t *testing.T) {
	t.Parallel()
	store := failingTokenStore{storagetest.NewTestTokenStore()}
	service, err := NewScrubbingService(context.Background(), lenientConfig(), testutil.NewTestLogger(), store)
	require.NoError(t, err)

	result := service.ScrubText(context.Background(), "password=secretvalue123")
	assert.Equal(t, "password="+untokenizedValue, result)
}

func TestScrubbingService_CustomScrubPatterns(t *testing.T) {
	t.Parallel()
	config := lenientConfig()
	config.CustomScrubPatterns = map[string]string{"internal_id": `INT-\d{6}`}
	service := newTestService(t, config)

	result := assertTokenized(t, service, "Processing INT-123456", "INT-123456")
	assert.Regexp(t, `^Processing \{\{UEI_[0-9a-f]{12}_\d+\}\}$`, result)
}

func TestScrubbingService_StrictModeDataRows(t *testing.T) {
	t.Parallel()
	service := newTestService(t, DefaultConfig())

	t.Run("tabular data keeps structure and tokenizes emails", func(t *testing.T) {
		t.Parallel()
		result := assertTokenized(t, service, "id\tname\temail\n1\tJohn\tjohn@test.com\n2\tJane\tjane@test.com", "john@test.com", "jane@test.com")
		assert.True(t, strings.HasPrefix(result, "id\tname\temail\n1\tJohn\t"))
	})

	t.Run("sensitive keys stay visible with tokenized values", func(t *testing.T) {
		t.Parallel()
		result := assertTokenized(t, service, "salary_info: 75000\nincome_data: 4430", "75000", "4430")
		assert.Regexp(t, `(?m)^salary_info: \{\{UEI_[0-9a-f]{12}_\d+\}\}$`, result)
		assert.Regexp(t, `(?m)^income_data: \{\{UEI_[0-9a-f]{12}_\d+\}\}$`, result)
	})

	t.Run("json sensitive key keeps key and quotes", func(t *testing.T) {
		t.Parallel()
		result := assertTokenized(t, service, `{"password":"hunter2","user":"svc-deploy"}`, "hunter2")
		assert.Regexp(t, `^\{"password":"\{\{UEI_[0-9a-f]{12}_\d+\}\}","user":"svc-deploy"\}$`, result)
	})

	t.Run("json email value tokenized, other fields kept", func(t *testing.T) {
		t.Parallel()
		result := assertTokenized(t, service, `{"user": "admin", "email": "admin@test.com"}`, "admin@test.com")
		assert.Contains(t, result, `"user": "admin"`)
		assert.Contains(t, result, `"email": "{{UEI_`)
	})

	t.Run("comment lines are left as written", func(t *testing.T) {
		t.Parallel()
		assertPreserved(t, service, "# account balance report")
	})
}

// A JSON object or array is scrubbed token by token: the result is valid JSON
// with its key order and layout, and only the scrubbed scalars change.
func TestScrubbingService_ScrubText_JSONStaysValid(t *testing.T) {
	t.Parallel()
	service := newTestService(t, DefaultConfig())
	ctx := context.Background()

	t.Run("machine numbers are not read as phone or card numbers", func(t *testing.T) {
		t.Parallel()
		assertPreserved(t, service,
			`{"stage_id":"s1","monotonic_start_ns":5551234567,"monotonic_end_ns":4111111111111111,"outcome":1}`,
			`[1792605197, 5551234567]`,
		)
	})

	t.Run("layout and key order are kept", func(t *testing.T) {
		t.Parallel()
		assertPreserved(t, service, "{\n  \"zeta\": \"a <b> & c\",\n  \"alpha\": [true, null, 2.5e3]\n}")
		result := assertTokenized(t, service, "{\n  \"zeta\": \"admin@test.com\",\n  \"alpha\": 1\n}", "admin@test.com")
		assert.Regexp(t, `^\{\n  "zeta": "\{\{UEI_[0-9a-f]{12}_\d+\}\}",\n  "alpha": 1\n\}$`, result)
	})

	t.Run("sensitive values are tokenized whole and stay JSON", func(t *testing.T) {
		t.Parallel()
		input := `{"secret":{"inner":"plain"},"prompt_tokens":123,"tokens":["alpha beta","gamma"],"password":"hunter2 with spaces","list":[{"token":"x1"}],"note":"line one\nline two"}`
		result := service.ScrubText(ctx, input)
		require.True(t, json.Valid([]byte(result)), result)
		for _, secret := range []string{"123", "alpha beta", "gamma", "hunter2", `"x1"`} {
			assert.NotContains(t, result, secret)
		}
		assert.Contains(t, result, `"secret":{"inner":"plain"}`, "an object under a sensitive key is scrubbed by its own keys")
		assert.Regexp(t, `"prompt_tokens":"\{\{UEI_[0-9a-f]{12}_\d+\}\}"`, result)
		assert.Contains(t, result, `"note":"line one\nline two"`)
	})

	t.Run("a JSON document inside a string is scrubbed structurally", func(t *testing.T) {
		t.Parallel()
		input := `{"content":"{\"api_key\":\"abc123\",\"count\":5551234567}"}`
		result := service.ScrubText(ctx, input)
		var outer struct {
			Content string `json:"content"`
		}
		require.NoError(t, json.Unmarshal([]byte(result), &outer))
		require.True(t, json.Valid([]byte(outer.Content)), outer.Content)
		assert.NotContains(t, outer.Content, "abc123")
		assert.Contains(t, outer.Content, `"count":5551234567`)
	})

	t.Run("key-anchored patterns still see their key", func(t *testing.T) {
		t.Parallel()
		lenient := newTestService(t, lenientConfig())
		secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		result := assertTokenized(t, lenient, `{"aws_secret_access_key":"`+secret+`"}`, secret)
		assert.True(t, json.Valid([]byte(result)), result)
	})

	t.Run("rescrubbing changes nothing", func(t *testing.T) {
		t.Parallel()
		once := service.ScrubText(ctx, `{"password":"hunter2","prompt_tokens":7,"email":"a@b.io"}`)
		assert.Equal(t, once, service.ScrubText(ctx, once))
	})
}

func TestScrubbingService_TokenPersistence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := testutil.NewTestLogger()
	tokenStore := storagetest.NewTestTokenStore()
	service, err := NewScrubbingService(ctx, lenientConfig(), logger, tokenStore)
	require.NoError(t, err)

	sensitiveValue := "my-secret-api-key-12345"
	token, err := service.GetTokenForValue(ctx, sensitiveValue)
	require.NoError(t, err)
	assert.Regexp(t, `^\{\{UEI_[0-9a-f]{12}_\d+\}\}$`, token)
	assert.Equal(t, sensitiveValue, mustRehydrateText(t, service, token))

	storedValue, err := tokenStore.KVGet(ctx, constants.ScrubbingTokenKeyPrefix+token)
	require.NoError(t, err)
	assert.Equal(t, sensitiveValue, storedValue)

	// A fresh service backed by the same store rehydrates from persistence and
	// continues the token sequence instead of reusing a persisted token.
	service2, err := NewScrubbingService(ctx, lenientConfig(), logger, tokenStore)
	require.NoError(t, err)
	assert.Equal(t, sensitiveValue, mustRehydrateText(t, service2, token))
	token2, err := service2.GetTokenForValue(ctx, "another-secret")
	require.NoError(t, err)
	assert.NotEqual(t, token, token2)
	assert.Equal(t, service.tokenOwner, service2.tokenOwner, "a restart keeps the runtime's owner namespace")
}

// The owner namespace is the runtime's identity for its tokens: a store that
// cannot yield a well-formed one fails construction instead of minting tokens
// under an owner the runtime cannot keep.
func TestNewScrubbingService_TokenOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("unreadable store", func(t *testing.T) {
		t.Parallel()
		storeErr := errors.New("vault is locked")
		_, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), unreadableTokenStore{err: storeErr, ownerReadable: false})
		require.ErrorIs(t, err, constants.ErrScrubbingTokenOwner)
		require.ErrorIs(t, err, storeErr)
	})

	t.Run("malformed persisted owner", func(t *testing.T) {
		t.Parallel()
		store := storagetest.NewTestTokenStore()
		require.NoError(t, store.KVSet(ctx, constants.ScrubbingTokenOwnerKey, "not-an-owner", 0))
		_, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), store)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenOwner)
	})

	t.Run("owner write failure", func(t *testing.T) {
		t.Parallel()
		_, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), unwritableTokenStore{storagetest.NewTestTokenStore()})
		require.ErrorIs(t, err, constants.ErrScrubbingTokenOwner)
	})
}

// unwritableTokenStore fails every write, the owner namespace included.
type unwritableTokenStore struct{ *storagetest.TestTokenStore }

func (unwritableTokenStore) KVSet(context.Context, string, string, int) error {
	return fmt.Errorf("kv set: %w", constants.ErrScrubbingTokenPersist)
}

// Persisted tokens from another owner, or with no owner, are never loaded.
func TestScrubbingService_LoadPersistedTokens_SkipsForeign(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := storagetest.NewTestTokenStore()
	owner, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), store)
	require.NoError(t, err)
	own, err := owner.GetTokenForValue(ctx, "own-secret")
	require.NoError(t, err)
	require.NoError(t, store.KVSet(ctx, constants.ScrubbingTokenKeyPrefix+"{{UEI_7}}", "ownerless-secret", 0))
	require.NoError(t, store.KVSet(ctx, constants.ScrubbingTokenKeyPrefix+"{{UEI_000000000000_9}}", "foreign-secret", 0))

	restarted, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), store)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{own: "own-secret"}, restarted.tokenMap)
	assert.Equal(t, 1, restarted.tokenSequence)
}

// Tokens minted by another service on the same store are loaded on demand.
func TestScrubbingService_RehydrateText_LoadsFromStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := testutil.NewTestLogger()
	tokenStore := storagetest.NewTestTokenStore()

	reader, err := NewScrubbingService(ctx, lenientConfig(), logger, tokenStore)
	require.NoError(t, err)
	writer, err := NewScrubbingService(ctx, lenientConfig(), logger, tokenStore)
	require.NoError(t, err)

	scrubbed := writer.ScrubText(ctx, "password=late-secret")
	assert.Equal(t, "password=late-secret", mustRehydrateText(t, reader, scrubbed))
}

// Two runtimes each mint their first token. Neither may resolve the other's
// token, least of all to its own unrelated value.
func TestScrubbingService_RehydrateText_ForeignTokenNeverResolvesLocally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	data := newTestService(t, lenientConfig())
	inference := newTestService(t, lenientConfig())

	dataToken, err := data.GetTokenForValue(ctx, "data-runtime-secret")
	require.NoError(t, err)
	inferenceToken, err := inference.GetTokenForValue(ctx, "inference-runtime-secret")
	require.NoError(t, err)
	require.Equal(t, 1, data.tokenSequence)
	require.Equal(t, 1, inference.tokenSequence)
	assert.NotEqual(t, dataToken, inferenceToken, "equal sequence numbers on two runtimes are distinct tokens")

	t.Run("rejected", func(t *testing.T) {
		t.Parallel()
		rehydrated, err := inference.RehydrateText(ctx, "value="+dataToken, ForeignTokenReject)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
		assert.Empty(t, rehydrated)
	})

	t.Run("opaque", func(t *testing.T) {
		t.Parallel()
		rehydrated, err := inference.RehydrateText(ctx, "summarize "+dataToken+" with "+inferenceToken, ForeignTokenOpaque)
		require.NoError(t, err)
		assert.Equal(t, "summarize "+dataToken+" with inference-runtime-secret", rehydrated)
	})

	t.Run("ownerless token is foreign", func(t *testing.T) {
		t.Parallel()
		_, err := inference.RehydrateText(ctx, "cat {{UEI_1}}", ForeignTokenReject)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
		rehydrated, err := inference.RehydrateText(ctx, "cat {{UEI_1}}", ForeignTokenOpaque)
		require.NoError(t, err)
		assert.Equal(t, "cat {{UEI_1}}", rehydrated)
	})

	// Opaque applies only to foreign tokens: an own token that no longer
	// resolves still withholds the text.
	t.Run("own unresolvable token fails under opaque", func(t *testing.T) {
		t.Parallel()
		rehydrated, err := inference.RehydrateText(ctx, dataToken+" "+unmintedToken(inference), ForeignTokenOpaque)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
		assert.Empty(t, rehydrated)
	})

	t.Run("opaque JSON payload keeps foreign keys and values", func(t *testing.T) {
		t.Parallel()
		payload := `{"` + dataToken + `":"` + dataToken + `","own":"` + inferenceToken + `"}`
		rehydrated, err := inference.RehydratePayload(ctx, []byte(payload), ForeignTokenOpaque)
		require.NoError(t, err)
		assert.JSONEq(t, `{"`+dataToken+`":"`+dataToken+`","own":"inference-runtime-secret"}`, string(rehydrated))
	})

	// A foreign token in text the local runtime scrubs is never wrapped in a
	// local token, so its owner can still rehydrate it.
	t.Run("rescrubbing keeps a foreign token whole", func(t *testing.T) {
		t.Parallel()
		scrubbed := inference.ScrubText(ctx, "password="+dataToken)
		assert.Equal(t, "password="+dataToken, scrubbed)
		assert.Equal(t, "password=data-runtime-secret", mustRehydrateText(t, data, scrubbed))
	})
}

func TestScrubbingService_IsEnabled(t *testing.T) {
	t.Parallel()
	assert.True(t, newTestService(t, &Config{Enabled: true}).IsEnabled())
	assert.False(t, newTestService(t, &Config{Enabled: false}).IsEnabled())

	var nilService *ScrubbingService
	assert.False(t, nilService.IsEnabled())
}

func TestScrubbingService_RehydrateText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t, lenientConfig())

	t.Run("empty input returns empty", func(t *testing.T) {
		t.Parallel()
		rehydrated, err := service.RehydrateText(ctx, "", ForeignTokenReject)
		require.NoError(t, err)
		assert.Empty(t, rehydrated)
	})

	t.Run("no tokens returns original", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "This is normal text", mustRehydrateText(t, service, "This is normal text"))
	})

	t.Run("rehydrates known token", func(t *testing.T) {
		t.Parallel()
		token, err := service.GetTokenForValue(ctx, "secret-value")
		require.NoError(t, err)
		assert.Equal(t, "Command with secret-value", mustRehydrateText(t, service, "Command with "+token))
	})

	t.Run("restored values are not rescanned", func(t *testing.T) {
		t.Parallel()
		inner, err := service.GetTokenForValue(ctx, "inner-value")
		require.NoError(t, err)
		outer, err := service.GetTokenForValue(ctx, "literal "+inner)
		require.NoError(t, err)
		assert.Equal(t, "literal "+inner, mustRehydrateText(t, service, outer))
	})

	// A placeholder that cannot be resolved must never reach dispatch as text, so the
	// whole call fails and returns no text.
	t.Run("unknown token fails closed", func(t *testing.T) {
		t.Parallel()
		rehydrated, err := service.RehydrateText(ctx, "run "+unmintedToken(service), ForeignTokenReject)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
		assert.Empty(t, rehydrated)
	})

	t.Run("token store lookup failure fails closed", func(t *testing.T) {
		t.Parallel()
		storeErr := errors.New("vault is locked")
		failing, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), unreadableTokenStore{err: storeErr, ownerReadable: true})
		require.NoError(t, err)

		rehydrated, err := failing.RehydrateText(ctx, "password="+unmintedToken(failing), ForeignTokenReject)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
		require.ErrorIs(t, err, storeErr, "the store failure stays in the error chain")
		assert.Empty(t, rehydrated)
	})

	t.Run("token outside memory without a token store fails closed", func(t *testing.T) {
		t.Parallel()
		unpersisted, err := NewScrubbingService(ctx, &Config{Enabled: true}, testutil.NewTestLogger(), nil)
		require.NoError(t, err)

		rehydrated, err := unpersisted.RehydrateText(ctx, unmintedToken(unpersisted), ForeignTokenReject)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
		assert.Empty(t, rehydrated)
	})
}

// unreadableTokenStore is a TokenStore whose lookups fail, as a locked vault
// makes them. With ownerReadable it holds no owner namespace instead of failing
// that lookup, so construction mints one and only token lookups fail.
type unreadableTokenStore struct {
	err           error
	ownerReadable bool
}

func (f unreadableTokenStore) KVSet(context.Context, string, string, int) error { return nil }

func (f unreadableTokenStore) KVGet(_ context.Context, key string) (string, error) {
	if f.ownerReadable && key == constants.ScrubbingTokenOwnerKey {
		return "", constants.ErrKeyNotFound
	}
	return "", f.err
}

func (f unreadableTokenStore) KVScanPrefix(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestScrubbingService_RehydratePayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t, lenientConfig())

	t.Run("empty payload returns empty", func(t *testing.T) {
		t.Parallel()
		result, err := service.RehydratePayload(ctx, []byte{}, ForeignTokenReject)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("non-JSON payload uses text rehydration", func(t *testing.T) {
		t.Parallel()
		token, err := service.GetTokenForValue(ctx, "secret")
		require.NoError(t, err)
		result, err := service.RehydratePayload(ctx, []byte("Text with "+token), ForeignTokenReject)
		require.NoError(t, err)
		assert.Equal(t, "Text with secret", string(result))
	})

	t.Run("JSON payload rehydrates recursively", func(t *testing.T) {
		t.Parallel()
		token, err := service.GetTokenForValue(ctx, "secret")
		require.NoError(t, err)
		payload := []byte(`{"key": "value ` + token + `", "nested": {"data": "` + token + `"}}`)
		result, err := service.RehydratePayload(ctx, payload, ForeignTokenReject)
		require.NoError(t, err)

		var parsed struct {
			Key    string `json:"key"`
			Nested struct {
				Data string `json:"data"`
			} `json:"nested"`
		}
		require.NoError(t, json.Unmarshal(result, &parsed))
		assert.Equal(t, "value secret", parsed.Key)
		assert.Equal(t, "secret", parsed.Nested.Data)
	})

	t.Run("JSON scalar types remain unchanged", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"count":42,"enabled":true,"missing":null,"values":[1,false,null]}`)
		result, err := service.RehydratePayload(ctx, payload, ForeignTokenReject)
		require.NoError(t, err)
		assert.JSONEq(t, string(payload), string(result))
	})

	t.Run("unresolvable token fails the whole payload", func(t *testing.T) {
		t.Parallel()
		unminted := unmintedToken(service)
		for _, payload := range [][]byte{
			[]byte("Text with " + unminted),
			[]byte(`{"ok":"fine","bad":"` + unminted + `"}`),
			[]byte(`["fine",{"bad":["` + unminted + `"]}]`),
		} {
			result, err := service.RehydratePayload(ctx, payload, ForeignTokenOpaque)
			require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved, "payload: %s", payload)
			assert.Nil(t, result, "no partially rehydrated payload is returned: %s", payload)
		}
	})

	// A sensitive key is tokenized by ScrubText along with the rest of the text, so
	// rehydration must restore the key too, or the handler receives a placeholder name.
	t.Run("object key restored", func(t *testing.T) {
		t.Parallel()
		scrubbed := service.ScrubText(ctx, `{"bob@x.io":1,"nested":{"bob@x.io":"v"}}`)
		require.NotContains(t, scrubbed, "bob@x.io")

		result, err := service.RehydratePayload(ctx, []byte(scrubbed), ForeignTokenReject)
		require.NoError(t, err)
		assert.JSONEq(t, `{"bob@x.io":1,"nested":{"bob@x.io":"v"}}`, string(result))
	})

	t.Run("unresolvable object key fails the whole payload", func(t *testing.T) {
		t.Parallel()
		result, err := service.RehydratePayload(ctx, []byte(`{"ok":1,"`+unmintedToken(service)+`":2}`), ForeignTokenReject)
		require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
		assert.Nil(t, result, "no partially rehydrated payload is returned")
	})

	// Two keys that restore to the same name would overwrite one field with the other.
	t.Run("colliding object keys fail the whole payload", func(t *testing.T) {
		t.Parallel()
		token, err := service.GetTokenForValue(ctx, "bob@x.io")
		require.NoError(t, err)

		result, err := service.RehydratePayload(ctx, []byte(`{"bob@x.io":1,"`+token+`":2}`), ForeignTokenReject)
		require.ErrorIs(t, err, constants.ErrScrubbingKeyCollision)
		assert.Nil(t, result, "no payload with a dropped field is returned")
	})
}

func TestScrubbingService_GetTokenForValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t, lenientConfig())

	t.Run("empty value returns empty", func(t *testing.T) {
		t.Parallel()
		token, err := service.GetTokenForValue(ctx, "")
		require.NoError(t, err)
		assert.Empty(t, token)
	})

	t.Run("same value returns same token", func(t *testing.T) {
		t.Parallel()
		token1, err := service.GetTokenForValue(ctx, "my-secret")
		require.NoError(t, err)
		token2, err := service.GetTokenForValue(ctx, "my-secret")
		require.NoError(t, err)
		assert.Equal(t, token1, token2)
	})

	t.Run("different values return different tokens", func(t *testing.T) {
		t.Parallel()
		token1, err := service.GetTokenForValue(ctx, "secret1")
		require.NoError(t, err)
		token2, err := service.GetTokenForValue(ctx, "secret2")
		require.NoError(t, err)
		assert.NotEqual(t, token1, token2)
	})

	t.Run("persist failure returns error and rolls back", func(t *testing.T) {
		t.Parallel()
		store := failingTokenStore{storagetest.NewTestTokenStore()}
		failing, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), store)
		require.NoError(t, err)

		token, err := failing.GetTokenForValue(ctx, "secret")
		require.ErrorIs(t, err, constants.ErrScrubbingTokenPersist)
		assert.Empty(t, token)
		assert.Empty(t, failing.tokenMap)
		assert.Zero(t, failing.tokenSequence)
	})
}

// The scrubbed marker pattern recognises exactly the markers tokenize emits.
func TestScrubbedMarkerPattern(t *testing.T) {
	t.Parallel()
	assert.True(t, scrubbedMarkerPattern.MatchString("{{UEI_3f9a0c12be47_12}}"))
	assert.True(t, scrubbedMarkerPattern.MatchString("{{UEI_12}}"), "an ownerless token is still a token")
	assert.True(t, scrubbedMarkerPattern.MatchString(untokenizedValue))
	assert.False(t, scrubbedMarkerPattern.MatchString("{{UEI_}}"))
	assert.False(t, scrubbedMarkerPattern.MatchString("{{UEI_3F9A0C12BE47_1}}"))
	assert.False(t, regexp.MustCompile(`^`+scrubbedMarkerPattern.String()+`$`).MatchString("{{UEI_3f9a0c12be47_1}}x"))
}
