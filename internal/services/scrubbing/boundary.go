// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package scrubbing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
)

// tokenPattern matches {{UEI_N}} placeholders in rehydration.
var tokenPattern = regexp.MustCompile(`\{\{UEI_\d+\}\}`)

// ContainsToken reports whether payload holds a {{UEI_N}} token. A dispatcher
// with no ScrubbingService cannot rehydrate one and must withhold the payload.
func ContainsToken(payload []byte) bool {
	return tokenPattern.Match(payload)
}

// Config holds configuration for the Sovereign Execution Boundary
type Config struct {
	// Enabled controls whether scrubbing is active
	Enabled bool

	// StrictMode when true, aggressively scrubs anything that looks like data
	StrictMode bool

	// AllowedPatterns are regex patterns that should pass through unscrubbed
	AllowedPatterns []string

	// CustomScrubPatterns are additional patterns to scrub
	CustomScrubPatterns map[string]string

	// RequirePersistence when true, fails closed if TokenStore is unavailable
	RequirePersistence bool
}

// DefaultConfig returns sensible defaults for production use
func DefaultConfig() *Config {
	return &Config{
		Enabled:             true,
		StrictMode:          true,
		AllowedPatterns:     []string{},
		CustomScrubPatterns: map[string]string{},
		RequirePersistence:  true,
	}
}

// valueGroupName names the regex submatch that holds the sensitive value in a
// key-prefixed pattern. Only that submatch is tokenized, so the key stays
// visible; a pattern without it tokenizes the whole match.
const valueGroupName = "value"

// RegexScrubber replaces every sensitive value its pattern matches with the
// value's reversible {{UEI_N}} token.
type RegexScrubber struct {
	name    string
	pattern *regexp.Regexp
}

func newRegexScrubber(name string, pattern *regexp.Regexp) *RegexScrubber {
	return &RegexScrubber{name: name, pattern: pattern}
}

func (r *RegexScrubber) Name() string { return r.name }

// Scrub replaces each matched value with tokenize(value).
func (r *RegexScrubber) Scrub(input string, tokenize func(string) string) string {
	group := max(r.pattern.SubexpIndex(valueGroupName), 0)
	var out strings.Builder
	last := 0
	for _, m := range r.pattern.FindAllStringSubmatchIndex(input, -1) {
		start, end := m[2*group], m[2*group+1]
		if start < 0 {
			continue
		}
		out.WriteString(input[last:start])
		out.WriteString(tokenize(input[start:end]))
		last = end
	}
	if last == 0 {
		return input
	}
	out.WriteString(input[last:])
	return out.String()
}

// ScrubbingService handles data scrubbing at the boundary plane:
// - Scrubbing: Removes sensitive data before cloud transmission
// - Rehydration: Restores sensitive data at execution boundary
// - Token Persistence: Maintains {{UEI_N}} mappings across restarts
type ScrubbingService struct {
	config    *Config
	logger    *slog.Logger
	scrubbers []*RegexScrubber

	// Tokenized context state for data scrubbing
	tokenMu       sync.RWMutex
	tokenMap      map[string]string // {{UEI_1}} -> "sensitive value"
	reverseMap    map[string]string // "sensitive value" -> {{UEI_1}}
	tokenSequence int

	// Persistent storage for token maps
	tokenStore storage.TokenStore
}

// NewScrubbingService creates a new data scrubbing service.
// Returns an error if persisted token loading fails.
func NewScrubbingService(ctx context.Context, config *Config, logger *slog.Logger, tokenStore storage.TokenStore) (*ScrubbingService, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if config.Enabled && config.RequirePersistence && tokenStore == nil {
		return nil, fmt.Errorf("scrubbing: new service: %w", constants.ErrScrubbingTokenStoreUnavailable)
	}

	s := &ScrubbingService{
		config:     config,
		logger:     logger,
		tokenMap:   make(map[string]string),
		reverseMap: make(map[string]string),
		tokenStore: tokenStore,
	}

	s.initializeScrubbers(ctx)

	// Load persisted tokens if storage is available
	if s.tokenStore != nil {
		if err := s.loadPersistedTokens(ctx); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// initializeScrubbers sets up all the pattern-based scrubbers
// IMPORTANT: Order matters! More specific patterns must come before generic ones.
// The scrubbers are applied sequentially, so a generic pattern matching first
// will prevent the specific pattern from ever seeing the text.
func (s *ScrubbingService) initializeScrubbers(ctx context.Context) {
	s.scrubbers = []*RegexScrubber{
		// JWT (JSON Web Token) - specific base64.base64.base64 format
		newRegexScrubber(string(constants.AuthProviderJWT), regexp.MustCompile(`\beyJ[A-Za-z0-9_-]*\.eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*\b`)),
		// SendGrid API Key - looks like hostname (SG.xxx.xxx) so must come first
		newRegexScrubber("sendgrid_key", regexp.MustCompile(`\bSG\.[0-9A-Za-z_-]{22}\.[0-9A-Za-z_-]{43}\b`)),

		// GitHub Token - specific prefix pattern
		newRegexScrubber("github_token", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,}\b`)),
		// GCP API Key - specific AIza prefix
		newRegexScrubber("gcp_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)),
		// AWS Access Key ID - specific AKIA/ASIA prefix
		newRegexScrubber("aws_access_key", regexp.MustCompile(`\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)),
		// Slack Token - xoxb/xoxp/xoxs/xapp prefixes (enterprise comms)
		newRegexScrubber("slack_token", regexp.MustCompile(`\b(xoxb|xoxp|xoxs|xapp)-[0-9A-Za-z-]{24,}\b`)),
		// Okta API Token - 00 prefix with 40 alphanumeric chars (enterprise/gov identity)
		newRegexScrubber("okta_api_token", regexp.MustCompile(`\b00[A-Za-z0-9_-]{40}\b`)),
		// Azure AD Client Secret - 3+ chars ~ 34+ chars format (gov/healthcare Microsoft)
		newRegexScrubber("azure_client_secret", regexp.MustCompile(`\b[A-Za-z0-9]{3,8}~[A-Za-z0-9._-]{34,}\b`)),
		// Twilio Account SID - specific AC prefix with hex
		newRegexScrubber("twilio_sid", regexp.MustCompile(`\bAC[a-f0-9]{32}\b`)),
		// NPM Token - specific npm_ prefix
		newRegexScrubber("npm_token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)),
		// PyPI Token - specific pypi- prefix with base64
		newRegexScrubber("pypi_token", regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{50,}\b`)),
		// Discord Bot Token - specific format with dots
		newRegexScrubber("discord_token", regexp.MustCompile(`\b[MN][A-Za-z\d]{23,}\.[\w-]{6}\.[\w-]{27}\b`)),
		// Private key blocks - very specific format
		newRegexScrubber("private_key", regexp.MustCompile(`-----BEGIN[^-]+PRIVATE KEY-----[\s\S]*?-----END[^-]+PRIVATE KEY-----`)),

		// AWS Secret Key pattern in config/env
		newRegexScrubber("aws_secret_key", regexp.MustCompile(`(?i)aws.{0,20}secret.{0,20}['\"]?(?P<value>[0-9a-zA-Z/+=]{40})['\"]?`)),
		// Azure Client Secret / Service Principal
		newRegexScrubber("azure_secret", regexp.MustCompile(`(?i)azure.{0,20}(secret|password|key).{0,20}['"](?P<value>[A-Za-z0-9_\-\.~]{32,})['"]`)),
		// Generic OAuth Client Secret
		newRegexScrubber("oauth_secret", regexp.MustCompile(`(?i)(client.?secret|oauth.?secret)\s*[=:]\s*['"]?(?P<value>[A-Za-z0-9_\-]{20,})['"]?`)),
		// Heroku API Key
		newRegexScrubber("heroku_key", regexp.MustCompile(`(?i)heroku.{0,20}(api.?key|token).{0,20}['"]?(?P<value>[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})['"]?`)),

		// URLs with embedded credentials (user:pass@host) - the credentials are tokenized, the host stays visible
		newRegexScrubber("url_with_creds", regexp.MustCompile(`https?://(?P<value>[^:/\s@]+:[^@\s/]+)@[^\s<>"{}|\\^`+"`"+`\[\]]+`)),
		// Connection strings (must come before email for same reason)
		newRegexScrubber("conn_string", regexp.MustCompile(`(?i)(?:mysql|postgres|mongodb|redis|amqp|jdbc)://[^\s]+`)),
		// Email addresses - a local part is required, so the host after an
		// already-tokenized URL credential stays visible
		newRegexScrubber("email", regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)),

		// Credit card numbers
		newRegexScrubber("credit_card", regexp.MustCompile(`\b(?:\d{4}[- ]?){3}\d{4}\b`)),
		// SSN
		newRegexScrubber("ssn", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)),
		// Phone numbers
		newRegexScrubber("phone", regexp.MustCompile(`\b(?:\+\d{1,3}[- ]?)?\(?\d{3}\)?[- ]?\d{3}[- ]?\d{4}\b`)),
		// Password patterns in config (generic catch-all)
		newRegexScrubber("password_config", regexp.MustCompile(`(?i)(?:password|passwd|pwd|secret|token|api_key|apikey)\s*[=:]\s*(?P<value>\S+)`)),
		// IBAN (International Bank Account Number) - covers 70+ countries
		newRegexScrubber("iban", regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{4,30}\b`)),
		// Generic Bearer Token in headers
		newRegexScrubber("bearer_token", regexp.MustCompile(`(?i)bearer\s+(?P<value>[a-zA-Z0-9_\-\.]+)`)),
	}

	for name, pattern := range s.config.CustomScrubPatterns {
		compileCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		compiled, err := compileRegexWithTimeout(compileCtx, pattern)
		cancel()
		if err != nil {
			s.logger.Warn("scrubbing: failed to compile custom pattern", "name", name, "error", err)
			continue
		}
		s.scrubbers = append(s.scrubbers, newRegexScrubber("custom_"+name, compiled))
	}
}

// compileRegexWithTimeout compiles a regex pattern with a timeout to prevent
// malicious or malformed patterns from blocking startup indefinitely
func compileRegexWithTimeout(ctx context.Context, pattern string) (*regexp.Regexp, error) {
	type result struct {
		re  *regexp.Regexp
		err error
	}

	resultChan := make(chan result, 1)

	go func() {
		re, err := regexp.Compile(pattern)
		select {
		case resultChan <- result{re: re, err: err}:
		case <-ctx.Done():
			// Context cancelled, don't block sending to channel
		}
	}()

	select {
	case res := <-resultChan:
		return res.re, res.err
	case <-ctx.Done():
		return nil, fmt.Errorf("scrubbing: compile regex: %w: %w", constants.ErrScrubbingRegexTimeout, ctx.Err())
	}
}

// suppressedOutput replaces all text when scrubbing is disabled.
const suppressedOutput = "[OUTPUT_SUPPRESSED]"

// untokenizedValue stands in for a sensitive value whose token could not be
// persisted. The value never passes through in plaintext.
const untokenizedValue = "{{UEI_UNAVAILABLE}}"

// ScrubText replaces every sensitive value in arbitrary text with its
// reversible {{UEI_N}} token. The surrounding text, including the key of a
// key-value pair, stays as written.
func (s *ScrubbingService) ScrubText(ctx context.Context, input string) string {
	if !s.IsEnabled() {
		return suppressedOutput
	}

	tokenize := func(value string) string { return s.tokenize(ctx, value) }
	result := input
	for _, scrubber := range s.scrubbers {
		result = scrubber.Scrub(result, tokenize)
	}

	// In strict mode, also tokenize the values of inherently sensitive keys
	if s.config.StrictMode {
		result = s.scrubDataValues(result, tokenize)
	}

	return result
}

// tokenize returns the tokenized form of one sensitive value. Tokens already in
// the value (from an earlier, overlapping scrubber) are kept as they are and
// only the text between them is tokenized, so a token never wraps another one:
// rehydration stays exact and rescrubbing scrubbed text is idempotent.
func (s *ScrubbingService) tokenize(ctx context.Context, value string) string {
	var out strings.Builder
	last := 0
	for _, loc := range scrubbedMarkerPattern.FindAllStringIndex(value, -1) {
		out.WriteString(s.tokenizeSegment(ctx, value[last:loc[0]]))
		out.WriteString(value[loc[0]:loc[1]])
		last = loc[1]
	}
	out.WriteString(s.tokenizeSegment(ctx, value[last:]))
	return out.String()
}

// tokenizeSegment replaces the text of a token-free segment with its token,
// keeping the segment's surrounding whitespace.
func (s *ScrubbingService) tokenizeSegment(ctx context.Context, segment string) string {
	core := strings.TrimSpace(segment)
	if core == "" {
		return segment
	}
	token, err := s.GetTokenForValue(ctx, core)
	if err != nil {
		s.logger.Error("scrubbing: tokenize sensitive value", "error", err)
		token = untokenizedValue
	}
	start := strings.Index(segment, core)
	return segment[:start] + token + segment[start+len(core):]
}

// scrubbedMarkerPattern matches a {{UEI_N}} token or the untokenized marker.
var scrubbedMarkerPattern = regexp.MustCompile(`\{\{UEI_(?:\d+|UNAVAILABLE)\}\}`)

// keyValuePairPattern matches one `key=value`, `Key: value` or `"key": value`
// pair. Group 1 is the left boundary, group 2 the key identifier (bare or
// quoted), group 3 the separator, and group 4 the value (quoted or bare). A
// bare value consumes an existing token whole.
var keyValuePairPattern = regexp.MustCompile(`(^|[\s,;{(\[&?])("[A-Za-z_][A-Za-z0-9_.\-]*"|'[A-Za-z_][A-Za-z0-9_.\-]*'|[A-Za-z_][A-Za-z0-9_.\-]*)(\s*=\s*|\s*:\s*)("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|(?:\{\{UEI_(?:\d+|UNAVAILABLE)\}\}|[^\s,;&}\]])+)`)

// scrubDataValues tokenizes, in place, the value of every key-value pair whose
// key is inherently sensitive (passwords, secrets, etc.), leaving the key and
// the rest of the line untouched.
func (s *ScrubbingService) scrubDataValues(input string, tokenize func(string) string) string {
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines[i] = s.tokenizeSensitivePairs(line, tokenize)
	}
	return strings.Join(lines, "\n")
}

// tokenizeSensitivePairs tokenizes the values of sensitive pairs within one
// line. A quoted value keeps its quotes. A bare value of a header-style
// `Key: value` pair tokenizes through the end of the line, since such values
// routinely contain spaces. A bare key followed by a colon without whitespace
// (a URL scheme, a time of day) is not a pair.
func (s *ScrubbingService) tokenizeSensitivePairs(line string, tokenize func(string) string) string {
	var out strings.Builder
	last := 0
	for _, m := range keyValuePairPattern.FindAllStringSubmatchIndex(line, -1) {
		key, sep := line[m[4]:m[5]], line[m[6]:m[7]]
		quotedKey := key[0] == '"' || key[0] == '\''
		if quotedKey {
			key = key[1 : len(key)-1]
		}
		headerStyle := !quotedKey && !strings.Contains(sep, "=")
		if headerStyle && strings.TrimRight(sep, " \t") == sep {
			continue
		}
		if !s.isLikelySensitiveKey(key) {
			continue
		}
		start, end := m[8], m[9]
		if quote := line[start]; (quote == '"' || quote == '\'') && end-start >= 2 {
			start, end = start+1, end-1
		} else if headerStyle {
			end = len(line)
		}
		out.WriteString(line[last:start])
		out.WriteString(tokenize(line[start:end]))
		last = end
		if end == len(line) {
			break
		}
	}
	out.WriteString(line[last:])
	return out.String()
}

// isLikelySensitiveKey checks if a key name suggests sensitive data.
// It uses word boundaries and exact matches where possible to avoid false positives.
func (s *ScrubbingService) isLikelySensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))

	// Exact matches for highest precision
	exactMatches := map[string]bool{
		"pwd":             true,
		"token":           true,
		"key":             true,
		"ssn":             true,
		"iban":            true,
		"api_key":         true,
		"apikey":          true,
		"secret":          true,
		"password":        true,
		"passwd":          true,
		"private":         true,
		"credit":          true,
		"account":         true,
		"account_number":  true,
		"balance":         true,
		"account_balance": true,
		"admin_pwd":       true,
		"card":            true,
		"card_number":     true,
	}
	if exactMatches[lower] {
		return true
	}

	// Pattern matches with context
	sensitivePatterns := []string{
		"password", "passwd", "secret", "token", "credential",
		"auth", "api_key", "apikey", "access_token", "private_key",
		"credit_card", "ssn", "income", "salary", "private", "credit",
		"account", "balance", "pwd", "card",
	}

	for _, pattern := range sensitivePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	return false
}

// RehydrateText replaces every {{UEI_N}} placeholder with its original value.
// It runs right before dispatch to restore sensitive data hidden from the cloud,
// so it fails closed: a placeholder that cannot be resolved is an error, and the
// text is never returned with the placeholder left in it. A token resolves from
// memory first and then from the TokenStore, so tokens minted by another service
// on the same store, or before a restart, still rehydrate.
func (s *ScrubbingService) RehydrateText(ctx context.Context, input string) (string, error) {
	if input == "" {
		return input, nil
	}

	// One pass over the input: a restored value is never rescanned, so the
	// result does not depend on substitution order.
	var out strings.Builder
	last := 0
	for _, loc := range tokenPattern.FindAllStringIndex(input, -1) {
		value, err := s.resolveToken(ctx, input[loc[0]:loc[1]])
		if err != nil {
			return "", fmt.Errorf("scrubbing: rehydrate text: %w", err)
		}
		out.WriteString(input[last:loc[0]])
		out.WriteString(value)
		last = loc[1]
	}
	out.WriteString(input[last:])
	return out.String(), nil
}

// resolveToken returns the sensitive value behind one {{UEI_N}} token. A token that
// is in neither memory nor the TokenStore, or whose store lookup fails, returns an
// error wrapping ErrScrubbingTokenUnresolved.
func (s *ScrubbingService) resolveToken(ctx context.Context, token string) (string, error) {
	s.tokenMu.RLock()
	value, ok := s.tokenMap[token]
	s.tokenMu.RUnlock()
	if ok {
		return value, nil
	}
	if s.tokenStore == nil {
		return "", fmt.Errorf("scrubbing: resolve %s: %w: no TokenStore", token, constants.ErrScrubbingTokenUnresolved)
	}

	value, err := s.tokenStore.KVGet(ctx, constants.ScrubbingTokenKeyPrefix+token)
	if err != nil {
		return "", fmt.Errorf("scrubbing: resolve %s: %w: %w", token, constants.ErrScrubbingTokenUnresolved, err)
	}
	s.tokenMu.Lock()
	s.tokenMap[token] = value
	s.reverseMap[value] = token
	s.tokenMu.Unlock()
	return value, nil
}

// RehydratePayload recursively rehydrates all string values and object keys in a JSON payload.
// json.RawMessage preserves arbitrary JSON shapes without introducing an
// untyped object model at this serialization boundary. Any unresolvable
// placeholder fails the whole payload: no partially rehydrated payload is returned.
func (s *ScrubbingService) RehydratePayload(ctx context.Context, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return payload, nil
	}

	var data json.RawMessage
	if err := json.Unmarshal(payload, &data); err != nil {
		// A payload that is not JSON is rehydrated as plain text.
		text, rehydrateErr := s.RehydrateText(ctx, string(payload))
		if rehydrateErr != nil {
			return nil, fmt.Errorf("scrubbing: rehydrate text payload: %w", rehydrateErr)
		}
		//nolint:nilerr // intentional fallback: the JSON decode error means plain text, not a failure
		return []byte(text), nil
	}

	return s.rehydrateJSONValue(ctx, data)
}

func (s *ScrubbingService) rehydrateJSONValue(ctx context.Context, value json.RawMessage) ([]byte, error) {
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return append([]byte(nil), value...), nil
	}

	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		rehydrated, rehydrateErr := s.RehydrateText(ctx, text)
		if rehydrateErr != nil {
			return nil, fmt.Errorf("scrubbing: rehydrate string value: %w", rehydrateErr)
		}
		return json.Marshal(rehydrated)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err == nil {
		fields := make(map[string]json.RawMessage, len(object))
		for key, child := range object {
			restoredKey, err := s.RehydrateText(ctx, key)
			if err != nil {
				return nil, fmt.Errorf("scrubbing: rehydrate object key: %w", err)
			}
			// Two distinct keys that restore to the same name would silently
			// overwrite one field with the other, so the payload is withheld.
			// The key text stays out of the error: a restored key is plaintext.
			if _, exists := fields[restoredKey]; exists {
				return nil, fmt.Errorf("scrubbing: rehydrate object key: %w", constants.ErrScrubbingKeyCollision)
			}
			rehydrated, err := s.rehydrateJSONValue(ctx, child)
			if err != nil {
				return nil, fmt.Errorf("scrubbing: rehydrate object field %q: %w", key, err)
			}
			fields[restoredKey] = rehydrated
		}
		return json.Marshal(fields)
	}

	var array []json.RawMessage
	if err := json.Unmarshal(value, &array); err == nil {
		for index, child := range array {
			rehydrated, err := s.rehydrateJSONValue(ctx, child)
			if err != nil {
				return nil, fmt.Errorf("scrubbing: rehydrate array element %d: %w", index, err)
			}
			array[index] = rehydrated
		}
		return json.Marshal(array)
	}

	return append([]byte(nil), value...), nil
}

// GetTokenForValue registers a sensitive value and returns its stable {{UEI_N}}
// token. Fails closed if persistence is required but unavailable or fails.
func (s *ScrubbingService) GetTokenForValue(ctx context.Context, value string) (string, error) {
	if value == "" {
		return "", nil
	}

	// Fail-closed: if persistence is required but unavailable, reject the operation
	if s.config.RequirePersistence && s.tokenStore == nil {
		return "", fmt.Errorf("scrubbing: tokenize value: %w", constants.ErrScrubbingTokenStoreUnavailable)
	}

	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()

	if token, ok := s.reverseMap[value]; ok {
		return token, nil
	}

	s.tokenSequence++
	token := fmt.Sprintf("{{UEI_%d}}", s.tokenSequence)
	s.tokenMap[token] = value
	s.reverseMap[value] = token

	// Persist to storage if available (24 hour TTL)
	if s.tokenStore != nil {
		const tokenTTLSeconds = 24 * 60 * 60
		key := constants.ScrubbingTokenKeyPrefix + token
		if err := s.tokenStore.KVSet(ctx, key, value, tokenTTLSeconds); err != nil {
			// Rollback the in-memory token since persistence failed
			delete(s.tokenMap, token)
			delete(s.reverseMap, value)
			s.tokenSequence--
			return "", fmt.Errorf("scrubbing: tokenize value %s: %w: %w", token, constants.ErrScrubbingTokenPersist, err)
		}
	}

	return token, nil
}

// IsEnabled returns whether scrubbing is active
// A nil receiver is disabled, so a typed-nil service fails closed instead of panicking.
func (s *ScrubbingService) IsEnabled() bool {
	return s != nil && s.config != nil && s.config.Enabled
}

// loadPersistedTokens loads tokens from TokenStore on startup.
// Returns an error if the scan fails. Individual token parse failures are logged
// and skipped, but the scan-level error is fatal.
func (s *ScrubbingService) loadPersistedTokens(ctx context.Context) error {
	if s.tokenStore == nil {
		s.logger.Warn("TokenStore not available for token persistence")
		return nil
	}

	tokens, err := s.tokenStore.KVScanPrefix(ctx, constants.ScrubbingTokenKeyPrefix)
	if err != nil {
		return fmt.Errorf("scrubbing: load persisted tokens: %w: %w", constants.ErrScrubbingTokenLoad, err)
	}

	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()

	loadedCount := 0
	maxSequence := 0
	for key, value := range tokens {
		// Extract token from key format: uei_token_{{UEI_N}}
		token := strings.TrimPrefix(key, constants.ScrubbingTokenKeyPrefix)
		if token == key {
			s.logger.Warn("scrubbing: invalid token key format", "key", key, "error", constants.ErrScrubbingTokenKeyFormat)
			continue
		}

		// Parse sequence number from token: {{UEI_N}}
		var seq int
		_, err := fmt.Sscanf(token, "{{UEI_%d}}", &seq)
		if err != nil {
			s.logger.Warn("scrubbing: failed to parse token sequence", "token", token, "error", fmt.Errorf("%w: %w", constants.ErrScrubbingTokenSequence, err))
			continue
		}

		if seq > maxSequence {
			maxSequence = seq
		}

		s.tokenMap[token] = value
		s.reverseMap[value] = token
		loadedCount++
	}

	s.tokenSequence = maxSequence
	s.logger.Info("Loaded persisted tokens from TokenStore", "count", loadedCount, "next_sequence", s.tokenSequence+1)
	return nil
}

// TokenKeymapHash computes a deterministic SHA-256 over the current token keymap.
// This hash is bound into the state Merkle root so that a rehydration substitution
// (altering the token→value mapping after a transaction hash was computed) produces
// a broken transaction, not silent corruption.
//
// The hash is computed over sorted token→value pairs to ensure determinism.
func (s *ScrubbingService) TokenKeymapHash() string {
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()

	if len(s.tokenMap) == 0 {
		return ""
	}

	h := sha256.New()

	// Sort tokens for deterministic ordering
	tokens := make([]string, 0, len(s.tokenMap))
	for token := range s.tokenMap {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)

	for _, token := range tokens {
		h.Write([]byte(token))
		h.Write([]byte(":"))
		h.Write([]byte(s.tokenMap[token]))
		h.Write([]byte("\n"))
	}

	return hex.EncodeToString(h.Sum(nil))
}
