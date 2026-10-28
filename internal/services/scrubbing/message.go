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
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// MessageContainsToken reports whether any string field or string map key of
// msg, at any depth, holds a UEI token. A dispatcher with no ScrubbingService
// cannot rehydrate one and must withhold the payload.
func MessageContainsToken(msg proto.Message) bool {
	found := false
	_, _ = walkMessageStrings(msg.ProtoReflect(), func(text string) (string, bool, error) {
		if ContainsToken([]byte(text)) {
			found = true
		}
		return text, false, nil
	})
	return found
}

// RehydrateMessage restores this runtime's UEI tokens in msg in place and
// reports whether any field changed. It rewrites the typed message, never its
// wire bytes: a restored value rarely has its token's length, so replacing it
// inside length-delimited protobuf bytes corrupts the payload.
//
// Every string field and string map key is visited, through nested messages,
// lists and maps. A string without a token keeps its exact value. A string
// that holds JSON (such as arguments_json) is rehydrated through the JSON
// reader, so a restored value is re-escaped; any other string is rehydrated as
// text. Bytes fields are never rewritten: no governed payload carries text in
// a bytes field, and evidence bytes must stay as recorded. Two map keys that
// restore to the same key fail with ErrScrubbingKeyCollision.
func (s *ScrubbingService) RehydrateMessage(ctx context.Context, msg proto.Message, foreign ForeignTokenPolicy) (bool, error) {
	return walkMessageStrings(msg.ProtoReflect(), func(text string) (string, bool, error) {
		if !ContainsToken([]byte(text)) {
			return text, false, nil
		}
		if json.Valid([]byte(text)) {
			rehydrated, err := s.rehydrateJSONValue(ctx, json.RawMessage(text), foreign)
			if err != nil {
				return "", false, err
			}
			return string(rehydrated), true, nil
		}
		rehydrated, err := s.RehydrateText(ctx, text, foreign)
		if err != nil {
			return "", false, err
		}
		return rehydrated, rehydrated != text, nil
	})
}

// stringRewrite returns the replacement for one string and whether it differs.
type stringRewrite func(text string) (string, bool, error)

// walkMessageStrings applies rewrite to every string field and string map key
// of m, in place, and reports whether any changed.
func walkMessageStrings(m protoreflect.Message, rewrite stringRewrite) (bool, error) {
	changed := false
	var walkErr error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		var fieldChanged bool
		switch {
		case fd.IsMap():
			fieldChanged, walkErr = walkMapStrings(fd, v.Map(), rewrite)
		case fd.IsList():
			list := v.List()
			for i := 0; i < list.Len() && walkErr == nil; i++ {
				var next protoreflect.Value
				var elementChanged bool
				next, elementChanged, walkErr = rewriteValue(fd, list.Get(i), rewrite)
				if elementChanged {
					list.Set(i, next)
					fieldChanged = true
				}
			}
		default:
			var next protoreflect.Value
			next, fieldChanged, walkErr = rewriteValue(fd, v, rewrite)
			if fieldChanged && fd.Kind() == protoreflect.StringKind {
				m.Set(fd, next)
			}
		}
		if walkErr != nil {
			walkErr = fmt.Errorf("field %s: %w", fd.Name(), walkErr)
			return false
		}
		changed = changed || fieldChanged
		return true
	})
	return changed, walkErr
}

// walkMapStrings rewrites a map's string keys and its string or message
// values. Entries are collected before any rewrite, because a map must not be
// modified while it is ranged over.
func walkMapStrings(fd protoreflect.FieldDescriptor, mp protoreflect.Map, rewrite stringRewrite) (bool, error) {
	type entry struct {
		key   protoreflect.MapKey
		value protoreflect.Value
	}
	entries := make([]entry, 0, mp.Len())
	mp.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		entries = append(entries, entry{key: k, value: v})
		return true
	})
	stringKeys := fd.MapKey().Kind() == protoreflect.StringKind
	changed := false
	rewritten := make(map[string]entry, len(entries))
	for _, e := range entries {
		key := e.key
		keyChanged := false
		if stringKeys {
			next, nextChanged, err := rewrite(e.key.String())
			if err != nil {
				return false, err
			}
			key, keyChanged = protoreflect.ValueOfString(next).MapKey(), nextChanged
		}
		value, valueChanged, err := rewriteValue(fd.MapValue(), e.value, rewrite)
		if err != nil {
			return false, err
		}
		if !keyChanged && !valueChanged {
			continue
		}
		changed = true
		mp.Clear(e.key)
		rewritten[e.key.String()] = entry{key: key, value: value}
	}
	for _, e := range rewritten {
		// A restored key that equals another key would silently overwrite one
		// entry with the other, so the payload is withheld. The key text stays
		// out of the error: a restored key is plaintext.
		if mp.Has(e.key) {
			return false, constants.ErrScrubbingKeyCollision
		}
		mp.Set(e.key, e.value)
	}
	return changed, nil
}

// rewriteValue rewrites one singular value: a string directly, a message in
// place. Every other kind is returned unchanged.
func rewriteValue(fd protoreflect.FieldDescriptor, v protoreflect.Value, rewrite stringRewrite) (protoreflect.Value, bool, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		next, changed, err := rewrite(v.String())
		if err != nil || !changed {
			return v, false, err
		}
		return protoreflect.ValueOfString(next), true, nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		changed, err := walkMessageStrings(v.Message(), rewrite)
		return v, changed, err
	default:
		return v, false, nil
	}
}
