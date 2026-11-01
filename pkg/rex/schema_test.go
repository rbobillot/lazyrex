package rex_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func fieldNames(fs []rex.Field) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func TestMethodSchema(t *testing.T) {
	resize, err := rex.MethodSchema(rextest.Fixture("api_describe_resize_pane.json"))
	if err != nil {
		t.Fatal(err)
	}
	fields, free, err := rex.SchemaFields(resize)
	if err != nil || free {
		t.Fatalf("resize fields: free=%v err=%v", free, err)
	}
	if got, want := fieldNames(fields), []string{"session_id", "block_id", "direction", "delta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resize fields = %q, want %q", got, want)
	}
	if d := fields[2]; d.Type != "string" || !d.Required || !reflect.DeepEqual(d.Enum, []string{"left", "right", "up", "down"}) {
		t.Fatalf("direction = %+v", d)
	}
	if fields[3].Type != "integer" {
		t.Fatalf("delta = %+v", fields[3])
	}

	inner := `{"type":"object","required":["b"],"properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`
	for _, wrap := range []string{`{"input_schema":%s}`, `{"params":%s}`, `{"args":%s}`, `{"input":%s}`, `{"request":%s}`, `%s`} {
		doc := []byte(fmt.Sprintf(wrap, inner))
		s, err := rex.MethodSchema(doc)
		if err != nil {
			t.Errorf("%s: %v", doc, err)
			continue
		}
		fs, _, err := rex.SchemaFields(s)
		if err != nil || !reflect.DeepEqual(fieldNames(fs), []string{"b", "a"}) {
			t.Errorf("%s: fields %q, %v", doc, fieldNames(fs), err)
		}
	}
	if _, err := rex.MethodSchema([]byte(`{"name":"x","output_schema":{}}`)); err == nil {
		t.Errorf("a describe without an input schema was accepted")
	}
	if _, err := rex.MethodSchema([]byte(`nope`)); err == nil {
		t.Errorf("non-JSON describe accepted")
	}
}

func TestSchemaFields(t *testing.T) {
	for _, empty := range []string{``, `null`, `{}`, `{"type":"object","properties":{}}`} {
		fs, free, err := rex.SchemaFields(json.RawMessage(empty))
		if err != nil || free || len(fs) != 0 {
			t.Errorf("%q: fields %v free %v err %v", empty, fs, free, err)
		}
	}
	if _, free, err := rex.SchemaFields(json.RawMessage(`{"type":"object","additionalProperties":true}`)); err != nil || !free {
		t.Errorf("schema without properties: free %v err %v", free, err)
	}
	fs, _, err := rex.SchemaFields(json.RawMessage(`{"properties":{"z":{"type":"string"},"m":{"type":["integer","null"]},"a":{}}}`))
	if err != nil || !reflect.DeepEqual(fieldNames(fs), []string{"a", "m", "z"}) || fs[1].Type != "integer" || fs[0].Required {
		t.Errorf("optional fields = %+v, %v", fs, err)
	}
	for name, bad := range map[string]string{
		"escape in name":  `{"properties":{"a\u001b[2Jb":{"type":"string"}}}`,
		"space in name":   `{"properties":{"a b":{"type":"string"}}}`,
		"dash name":       `{"properties":{"-e":{"type":"string"}}}`,
		"control in enum": `{"properties":{"k":{"type":"string","enum":["ok","a\u0007"]}}}`,
		"not an object":   `[1]`,
	} {
		if _, _, err := rex.SchemaFields(json.RawMessage(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	bad, err := rex.ParseActions(rextest.Fixture("client_actions_badschema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rex.SchemaFields(bad[0].Args); err == nil {
		t.Errorf("client_actions_badschema.json accepted")
	}
}

func TestFieldValue(t *testing.T) {
	intF := rex.Field{Name: "n", Type: "integer"}
	cases := []struct {
		f    rex.Field
		in   string
		want any
		skip bool
		err  bool
	}{
		{f: intF, in: "5", want: int64(5)},
		{f: intF, in: " 5 ", want: int64(5)},
		{f: intF, in: "x", err: true},
		{f: rex.Field{Type: "number"}, in: "1.5", want: 1.5},
		{f: rex.Field{Type: "boolean"}, in: "true", want: true},
		{f: rex.Field{Type: "boolean"}, in: "maybe", err: true},
		{f: rex.Field{Type: "string", Enum: []string{"a", "b"}}, in: "c", err: true},
		{f: rex.Field{Type: "string", Enum: []string{"a", "b"}}, in: "b", want: "b"},
		{f: rex.Field{Type: "string"}, in: " keep spaces ", want: " keep spaces "},
		{f: rex.Field{Type: "string", Required: true}, in: "", err: true},
		{f: rex.Field{Type: "string"}, in: "", skip: true},
		{f: rex.Field{Type: "object"}, in: "{", err: true},
		{f: rex.Field{Type: "object"}, in: `{"a":1}`, want: json.RawMessage(`{"a":1}`)},
		{f: rex.Field{Type: "array"}, in: `[1,2]`, want: json.RawMessage(`[1,2]`)},
	}
	for _, c := range cases {
		v, skip, err := rex.FieldValue(c.f, c.in)
		if (err != nil) != c.err || skip != c.skip || (!c.err && !c.skip && !reflect.DeepEqual(v, c.want)) {
			t.Errorf("FieldValue(%+v, %q) = %#v, %v, %v", c.f, c.in, v, skip, err)
		}
	}
}

func TestBuildArgs(t *testing.T) {
	if got := rex.BuildArgs(nil); got != "" {
		t.Errorf("BuildArgs(nil) = %q", got)
	}
	got := rex.BuildArgs(map[string]any{"path": "/x<y>", "line": int64(7), "mode": "edit", "o": json.RawMessage(`{"a":1}`)})
	if want := `{"line":7,"mode":"edit","o":{"a":1},"path":"/x<y>"}`; got != want {
		t.Errorf("BuildArgs = %s, want %s", got, want)
	}
}

func TestCompleteField(t *testing.T) {
	if f, err := rex.CompleteField(rextest.Fixture("api_describe_target_complete.json")); err != nil || f != "prefix" {
		t.Fatalf("CompleteField = %q, %v", f, err)
	}
	cases := map[string]string{
		`{"input_schema":{"properties":{"n":{"type":"integer"},"text":{"type":"string"}}}}`:                   "text",
		`{"input_schema":{"required":["q"],"properties":{"q":{"type":"string"},"prefix":{"type":"string"}}}}`: "q",
		`{"input_schema":{"properties":{"n":{"type":"integer"},"other":{"type":"string"}}}}`:                  "",
	}
	for doc, want := range cases {
		f, err := rex.CompleteField([]byte(doc))
		if f != want || (want == "") != (err != nil) {
			t.Errorf("CompleteField(%s) = %q, %v; want %q", doc, f, err, want)
		}
	}
}
