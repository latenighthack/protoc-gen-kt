package main

import (
	"strings"
	"testing"
)

const validateHeader = `syntax = "proto3";
package v.t;
import "buf/validate/validate.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/timestamp.proto";
option java_package = "v.t";
`

func validateFiles(body string) map[string]string {
	return map[string]string{"v.proto": validateHeader + body}
}

func TestValidateOffIgnoresRulesAndWritesNoValidateFile(t *testing.T) {
	// Includes a rule that is unsupported when validation is on: it must be ignored when off.
	files, output, err := generate(t, validateFiles(`message M { string s = 1 [(buf.validate.field).string.email = true, (buf.validate.field).cel = {id: "x", expression: "true"}]; }`))
	if err != nil {
		t.Fatalf("generation failed: %v\n%s", err, output)
	}
	for name := range files {
		if strings.Contains(name, "validate") && strings.HasSuffix(name, ".validate.kt") {
			t.Fatalf("unexpected validate output %s", name)
		}
	}
	if _, ok := files["v/t/v.data.kt"]; !ok {
		t.Fatalf("expected normal output, got %v", keys(files))
	}
}

func TestValidateOffMatchesExplicitFalse(t *testing.T) {
	a, _, err := generate(t, validateFiles(`message M { string s = 1 [(buf.validate.field).string.min_len = 1]; }`))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := generateWith(t, validateFiles(`message M { string s = 1 [(buf.validate.field).string.min_len = 1]; }`), "--kt_opt=validate=false")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("file sets differ: %v vs %v", keys(a), keys(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("%s differs between default and validate=false", k)
		}
	}
}

func TestValidateOnWritesValidateFile(t *testing.T) {
	files, output, err := generateWith(t, validateFiles(`
message Inner { string name = 1 [(buf.validate.field).string.min_len = 2]; }
message M {
  message Nested { int32 n = 1 [(buf.validate.field).int32.gt = 0]; }
  Inner inner = 1;
  repeated Inner items = 2;
  map<string, Inner> by = 3;
}`), "--kt_opt=validate=true")
	if err != nil {
		t.Fatalf("generation failed: %v\n%s", err, output)
	}
	body, ok := files["v/t/v.validate.kt"]
	if !ok {
		t.Fatalf("no validate file; got %v", keys(files))
	}
	for _, want := range []string{
		"fun v.t.Inner.validate(): List<Violation>",
		"fun v.t.Inner.validateOrThrow()",
		"fun v.t.M.Nested.validateInto(out: MutableList<Violation>, path: String)",
		"fun v.t.M.validateInto(out: MutableList<Violation>, path: String)",
		`"string.min_len"`,
		`"int32.gt"`,
		"e.validateInto(out, ep)",
		"v.validateInto(out, p)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("validate file missing %q\n%s", want, body)
		}
	}
	// Core outputs are unchanged by the option.
	plain, _, err := generate(t, validateFiles(`
message Inner { string name = 1 [(buf.validate.field).string.min_len = 2]; }
message M {
  message Nested { int32 n = 1 [(buf.validate.field).int32.gt = 0]; }
  Inner inner = 1;
  repeated Inner items = 2;
  map<string, Inner> by = 3;
}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range plain {
		if files[name] != content {
			t.Errorf("%s differs when validate=true", name)
		}
	}
}

func TestValidateOnRejectsUnsupportedRules(t *testing.T) {
	cases := []struct{ name, field, rule string }{
		{"email", `string s = 1 [(buf.validate.field).string.email = true]`, "string.email"},
		{"uuid", `string s = 1 [(buf.validate.field).string.uuid = true]`, "string.uuid"},
		{"uri", `string s = 1 [(buf.validate.field).string.uri = true]`, "string.uri"},
		{"hostname", `string s = 1 [(buf.validate.field).string.hostname = true]`, "string.hostname"},
		{"ip", `string s = 1 [(buf.validate.field).string.ip = true]`, "string.ip"},
		{"well_known_regex", `string s = 1 [(buf.validate.field).string.well_known_regex = KNOWN_REGEX_HTTP_HEADER_NAME]`, "string.well_known_regex"},
		{"cel", `string s = 1 [(buf.validate.field).cel = {id: "x", expression: "true"}]`, "cel"},
		{"cel_expression", `string s = 1 [(buf.validate.field).cel_expression = "true"]`, "cel_expression"},
		{"bytes_ip", `bytes s = 1 [(buf.validate.field).bytes.ip = true]`, "bytes.ip"},
		{"bytes_pattern", `bytes s = 1 [(buf.validate.field).bytes.pattern = "a"]`, "bytes.pattern"},
		{"any", `google.protobuf.Duration s = 1 [(buf.validate.field).duration.in = {seconds: 1}]`, "duration.in"},
		{"items_cel", `repeated string s = 1 [(buf.validate.field).repeated.items.cel = {id: "x", expression: "true"}]`, "repeated.items.cel"},
		{"items_email", `repeated string s = 1 [(buf.validate.field).repeated.items.string.email = true]`, "repeated.items.string.email"},
		{"map_values_email", `map<string,string> s = 1 [(buf.validate.field).map.values.string.email = true]`, "map.values.string.email"},
		{"ts_in_range_msg", `google.protobuf.Timestamp s = 1 [(buf.validate.field).timestamp.lt_now = true, (buf.validate.field).timestamp.example = {seconds: 1}, (buf.validate.field).cel_expression = "true"]`, "cel_expression"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, output, err := generateWith(t, validateFiles("message M { "+tc.field+"; }"), "--kt_opt=validate=true")
			if err == nil {
				t.Fatalf("expected generation to fail")
			}
			want := "v.proto: M.s: unsupported buf.validate rule " + tc.rule
			if !strings.Contains(output, want) {
				t.Fatalf("output %q lacks %q", output, want)
			}
		})
	}
}

func TestValidateOnRejectsUnsupportedMessageAndOneofRules(t *testing.T) {
	_, output, err := generateWith(t, validateFiles(`message M { option (buf.validate.message).cel = {id: "x", expression: "true"}; string s = 1; }`), "--kt_opt=validate=true")
	if err == nil || !strings.Contains(output, "v.proto: M: unsupported buf.validate rule cel") {
		t.Fatalf("message cel: err=%v output=%q", err, output)
	}
	_, output, err = generateWith(t, validateFiles(`message M { option (buf.validate.message).oneof = {fields: ["a","b"]}; string a = 1; string b = 2; }`), "--kt_opt=validate=true")
	if err == nil || !strings.Contains(output, "v.proto: M: unsupported buf.validate rule oneof") {
		t.Fatalf("message oneof: err=%v output=%q", err, output)
	}
}

func TestValidateOnRejectsNonPortablePatterns(t *testing.T) {
	bad := []string{`\\pL+`, `\\p{Greek}`, `\\bfoo\\b`, `\\s+`, `(?m)^a$`, `(a+)+$`, `(a*)*`, `(\\w+\\d*)+x`, `[a-z`, `(?P<n>a)\\k<n>`}
	for _, pattern := range bad {
		_, output, err := generateWith(t, validateFiles(`message M { string s = 1 [(buf.validate.field).string.pattern = "`+pattern+`"]; }`), "--kt_opt=validate=true")
		if err == nil || !strings.Contains(output, "v.proto: M.s: unsupported buf.validate rule string.pattern") {
			t.Errorf("pattern %q: err=%v output=%q", pattern, err, output)
		}
	}
}

func TestValidateOnAcceptsPortablePatterns(t *testing.T) {
	good := []string{`^[a-z0-9]+$`, `^(foo|bar)-\\d{2,3}$`, `^a.c$`, `(?i)^hello`, `foo`, `^[^a-z]+$`, `^a+?b*$`, `(?P<n>ab)c`, `[[:alpha:]]+`, `\\Aabc\\z`, `a{3}`}
	for _, pattern := range good {
		files, output, err := generateWith(t, validateFiles(`message M { string s = 1 [(buf.validate.field).string.pattern = "`+pattern+`"]; }`), "--kt_opt=validate=true")
		if err != nil {
			t.Errorf("pattern %q rejected: %v %s", pattern, err, output)
			continue
		}
		if !strings.Contains(files["v/t/v.validate.kt"], "private val ktbufValidateRegex1 = Regex(") {
			t.Errorf("pattern %q: no precompiled regex", pattern)
		}
	}
}

func TestRegexDollarIsEndOfInputNotBeforeTrailingNewline(t *testing.T) {
	files, _, err := generateWith(t, validateFiles(`message M { string s = 1 [(buf.validate.field).string.pattern = "^a$"]; }`), "--kt_opt=validate=true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files["v/t/v.validate.kt"], `Regex("^a(?![\\s\\S])")`) {
		t.Fatalf("unexpected regex translation:\n%s", files["v/t/v.validate.kt"])
	}
}

func TestUnknownParametersAreRejected(t *testing.T) {
	for _, opt := range []string{"--kt_opt=vaildate=true", "--kt_opt=validate=maybe", "--kt_opt=validate=true,bogus=1"} {
		_, output, err := generateWith(t, validateFiles(`message M { string s = 1; }`), opt)
		if err == nil {
			t.Errorf("%s: expected failure", opt)
			continue
		}
		if !strings.Contains(output, "unknown parameter") && !strings.Contains(output, "invalid value") {
			t.Errorf("%s: unhelpful error %q", opt, output)
		}
	}
}

func TestValidateOnStillRejectsProto2Targets(t *testing.T) {
	_, output, err := generateWith(t, map[string]string{"p2.proto": `syntax = "proto2"; package p; message M { optional string s = 1; }`}, "--kt_opt=validate=true")
	if err == nil || !strings.Contains(output, "unsupported presence semantics") {
		t.Fatalf("err=%v output=%q", err, output)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
