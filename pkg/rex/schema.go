package rex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Field is one argument of an action or API method, read from its JSON Schema.
type Field struct {
	Name        string
	Type        string
	Description string
	Required    bool
	Enum        []string
}

var schemaWrappers = []string{"input_schema", "params", "args", "input", "request"}

// MethodSchema returns the input schema in `api describe --json` output.
func MethodSchema(describe []byte) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(describe, &obj); err != nil {
		return nil, fmt.Errorf("parse api describe: %w", err)
	}
	for _, k := range schemaWrappers {
		if s := bytes.TrimSpace(obj[k]); len(s) > 0 && s[0] == '{' {
			return s, nil
		}
	}
	if _, ok := obj["properties"]; ok {
		return describe, nil
	}
	return nil, errors.New("api describe has no input schema")
}

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// SchemaFields lists the properties of an object schema, required ones first
// in schema order, then the others by name. free reports a schema that takes
// arguments without listing them.
func SchemaFields(schema json.RawMessage) (fields []Field, free bool, err error) {
	if s := bytes.TrimSpace(schema); len(s) == 0 || string(s) == "null" {
		return nil, false, nil
	}
	var s struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, false, fmt.Errorf("parse schema: %w", err)
	}
	if len(s.Properties) == 0 {
		ap := string(bytes.TrimSpace(s.AdditionalProperties))
		return nil, ap != "" && ap != "false", nil
	}
	for name, raw := range s.Properties {
		if !fieldName.MatchString(name) {
			return nil, false, fmt.Errorf("schema property %q is not a plain name", name)
		}
		f, err := schemaField(name, raw)
		if err != nil {
			return nil, false, err
		}
		f.Required = slices.Contains(s.Required, name)
		fields = append(fields, f)
	}
	rank := func(f Field) int {
		if i := slices.Index(s.Required, f.Name); i >= 0 {
			return i
		}
		return len(s.Required)
	}
	slices.SortFunc(fields, func(a, b Field) int {
		if r := rank(a) - rank(b); r != 0 {
			return r
		}
		return strings.Compare(a.Name, b.Name)
	})
	return fields, false, nil
}

func schemaField(name string, raw json.RawMessage) (Field, error) {
	var p struct {
		Type        json.RawMessage   `json:"type"`
		Description string            `json:"description"`
		Enum        []json.RawMessage `json:"enum"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Field{}, fmt.Errorf("schema property %s: %w", name, err)
	}
	f := Field{Name: name, Description: p.Description}
	var one string
	var many []string
	if json.Unmarshal(p.Type, &one) == nil {
		f.Type = one
	} else if json.Unmarshal(p.Type, &many) == nil {
		for _, t := range many {
			if t != "null" {
				f.Type = t
				break
			}
		}
	}
	for _, e := range p.Enum {
		var v string
		if json.Unmarshal(e, &v) != nil {
			v = string(bytes.TrimSpace(e))
		}
		if strings.ContainsFunc(v, unicode.IsControl) {
			return Field{}, fmt.Errorf("schema property %s has an enum value with control characters", name)
		}
		f.Enum = append(f.Enum, v)
	}
	return f, nil
}

// FieldValue converts what the user typed for f. skip reports an empty
// optional field, which is left out of the arguments.
func FieldValue(f Field, in string) (v any, skip bool, err error) {
	trimmed := strings.TrimSpace(in)
	text := trimmed
	if f.Type == "string" || f.Type == "" {
		text = in
	}
	if text == "" {
		if f.Required {
			return nil, false, fmt.Errorf("%s is required", f.Name)
		}
		return nil, true, nil
	}
	if len(f.Enum) > 0 && !slices.Contains(f.Enum, text) {
		return nil, false, fmt.Errorf("%s must be one of %s", f.Name, strings.Join(f.Enum, ", "))
	}
	switch f.Type {
	case "integer":
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("%s must be an integer", f.Name)
		}
		return n, false, nil
	case "number":
		n, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, false, fmt.Errorf("%s must be a number", f.Name)
		}
		return n, false, nil
	case "boolean":
		switch trimmed {
		case "true":
			return true, false, nil
		case "false":
			return false, false, nil
		}
		return nil, false, fmt.Errorf("%s must be true or false", f.Name)
	case "object", "array":
		open := map[string]byte{"object": '{', "array": '['}[f.Type]
		if trimmed[0] != open || !json.Valid([]byte(trimmed)) {
			return nil, false, fmt.Errorf("%s must be a JSON %s", f.Name, f.Type)
		}
		return json.RawMessage(trimmed), false, nil
	}
	return text, false, nil
}

// BuildArgs encodes form values as one JSON object, or "" when there are none.
func BuildArgs(values map[string]any) string {
	if len(values) == 0 {
		return ""
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(values); err != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

var completeFieldNames = []string{"prefix", "text", "input", "query", "partial", "word"}

// CompleteField picks the argument of target.complete that takes the typed
// prefix: its first required string, else a string field with a usual name.
func CompleteField(describe []byte) (string, error) {
	schema, err := MethodSchema(describe)
	if err != nil {
		return "", err
	}
	fields, _, err := SchemaFields(schema)
	if err != nil {
		return "", err
	}
	for _, f := range fields {
		if f.Required && f.Type == "string" {
			return f.Name, nil
		}
	}
	for _, name := range completeFieldNames {
		for _, f := range fields {
			if f.Name == name && f.Type == "string" {
				return f.Name, nil
			}
		}
	}
	return "", errors.New("target.complete takes no prefix argument")
}
