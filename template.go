package twin

import (
	"fmt"
	"os"
	"regexp"
)

var tokenPattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// TemplateFields are the grubber record fields that may contain {{tokens}}.
var TemplateFields = []string{"Source", "Target", "Path", "Target-Path", "Exclude", "Cmd"}

// Substitute replaces {{token}} in value using vars. Unknown tokens are an
// error, never silently left in place.
func Substitute(value string, vars map[string]string, context string) (string, error) {
	var firstErr error
	out := tokenPattern.ReplaceAllStringFunc(value, func(m string) string {
		token := m[2 : len(m)-2]
		v, ok := vars[token]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown template token {{%s}} in %s", token, context)
			}
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// RenderFile reads a template file and substitutes {{tokens}} in its
// content. Bytes outside tokens are preserved exactly.
func RenderFile(path string, vars map[string]string, context string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := Substitute(string(data), vars, context)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// SubstituteRecord returns a copy of grubber record r with TemplateFields
// substituted. With no vars the record is returned as is.
func SubstituteRecord(r map[string]any, vars map[string]string, context string) (map[string]any, error) {
	if len(vars) == 0 {
		return r, nil
	}
	out := make(map[string]any, len(r))
	for k, v := range r {
		out[k] = v
	}
	for _, f := range TemplateFields {
		s, ok := out[f].(string)
		if !ok {
			continue
		}
		sub, err := Substitute(s, vars, context)
		if err != nil {
			return nil, err
		}
		out[f] = sub
	}
	return out, nil
}
