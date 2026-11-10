// Package settings reads the opaque settings maps that obatala.yaml hands
// to transport and manager factories.
package settings

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// String returns settings[key] as a string ("" when absent).
func String(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// Required returns settings[key] or an error naming owner and key.
func Required(m map[string]any, owner, key string) (string, error) {
	s := String(m, key)
	if s == "" {
		return "", fmt.Errorf("%s: settings.%s is required", owner, key)
	}
	return s, nil
}

// Duration parses settings[key] ("30s", or a number of seconds); def when
// absent.
func Duration(m map[string]any, owner, key string, def time.Duration) (time.Duration, error) {
	switch v := m[key].(type) {
	case nil:
		return def, nil
	case string:
		if v == "" {
			return def, nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("%s: settings.%s: %w", owner, key, err)
		}
		return d, nil
	case int:
		return time.Duration(v) * time.Second, nil
	case float64:
		return time.Duration(v * float64(time.Second)), nil
	default:
		return 0, fmt.Errorf("%s: settings.%s: want a duration like \"30s\"", owner, key)
	}
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Headers reads settings[key] as a string map, expanding ${ENV_VAR}
// references so secrets stay in the environment (or .env), never in the
// YAML. A referenced variable that is unset is an error.
func Headers(m map[string]any, owner, key string) (map[string]string, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, nil
	}
	in, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: settings.%s must be a map of header names to values", owner, key)
	}
	out := make(map[string]string, len(in))
	for name, v := range in {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: settings.%s.%s must be a string", owner, key, name)
		}
		var missing []string
		expanded := envRef.ReplaceAllStringFunc(s, func(ref string) string {
			env := envRef.FindStringSubmatch(ref)[1]
			val, ok := os.LookupEnv(env)
			if !ok || val == "" {
				missing = append(missing, env)
			}
			return val
		})
		if len(missing) > 0 {
			return nil, fmt.Errorf("%s: settings.%s.%s references unset env %s", owner, key, name, strings.Join(missing, ", "))
		}
		out[name] = expanded
	}
	return out, nil
}
