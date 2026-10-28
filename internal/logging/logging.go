package logging

import (
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
)

// Redacted replaces every sensitive value in the logs.
const Redacted = "[REDACTED]"

// New returns a JSON logger that writes to w at level with the redaction
// rules applied.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(NewHandler(w, level))
}

// NewHandler returns a JSON handler that writes to w at level with the
// redaction rules applied.
func NewHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: Redact})
}

// Redact applies the redaction rules to one attribute. It has the signature
// of slog.HandlerOptions.ReplaceAttr, so other handlers can reuse the rules.
func Redact(groups []string, a slog.Attr) slog.Attr {
	if sensitiveKey(a.Key) || slices.ContainsFunc(groups, sensitiveKey) {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		if sensitiveString(a.Value.String()) {
			return slog.String(a.Key, Redacted)
		}
	case slog.KindAny:
		if m, ok := redactMap(a.Value.Any()); ok {
			return slog.Any(a.Key, m)
		}
	}
	return a
}

var sensitiveKeys = map[string]bool{
	"authorization":       true,
	"proxy_authorization": true,
	"cookie":              true,
	"set_cookie":          true,
	"token":               true,
	"subject":             true,
	"body":                true,
	"password":            true,
	"secret":              true,
	"api_key":             true,
	"database_url":        true,
}

var sensitiveSuffixes = []string{"_token", "_subject", "_body", "_password", "_secret", "_api_key"}

func sensitiveKey(key string) bool {
	k := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	if sensitiveKeys[k] {
		return true
	}
	for _, s := range sensitiveSuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

func sensitiveString(s string) bool {
	const bearer = "bearer "
	lower := strings.ToLower(s)
	return strings.HasPrefix(strings.TrimSpace(lower), bearer) || strings.Contains(lower, "token=")
}

// redactMap returns a copy of a string-keyed map with sensitive entries
// replaced, or false if v is not such a map or needs no change.
func redactMap(v any) (map[string]any, bool) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	out := make(map[string]any, rv.Len())
	changed := false
	for iter := rv.MapRange(); iter.Next(); {
		k := iter.Key().String()
		val := iter.Value().Interface()
		if sensitiveKey(k) || containsSensitiveString(iter.Value()) {
			val, changed = Redacted, true
		} else if m, ok := redactMap(val); ok {
			val, changed = m, true
		}
		out[k] = val
	}
	return out, changed
}

// containsSensitiveString reports whether v is a string, or a slice or array
// of strings (an http.Header value), that sensitiveString matches.
func containsSensitiveString(v reflect.Value) bool {
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.String:
		return sensitiveString(v.String())
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if containsSensitiveString(v.Index(i)) {
				return true
			}
		}
	}
	return false
}
