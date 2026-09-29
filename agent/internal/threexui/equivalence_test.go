package threexui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func requireConfigComparison(t *testing.T, expected, actual string) ConfigComparison {
	t.Helper()
	result, err := CompareConfig([]byte(expected), []byte(actual))
	if err != nil {
		t.Fatalf("compare fixture configurations: %v", err)
	}
	for _, digest := range []string{result.ExpectedDigest, result.ActualDigest} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size || digest != strings.ToLower(digest) {
			t.Fatal("comparison returned an invalid digest")
		}
	}
	return result
}

func TestCompareConfigIgnoresObjectKeyOrderAndFormatting(t *testing.T) {
	expected := `{"stats":{},"inbounds":[{"tag":"entry","settings":{"clients":[{"email":"fixture@example.invalid","id":"fixture-credential"}]}}],"custom":{"z":null,"a":"<雪>"}}`
	actual := " { \n" + `"custom":{"a":"\u003c\u96ea\u003e","z":null},"inbounds":[{"settings":{"clients":[{"id":"fixture-credential","email":"fixture@example.invalid"}]},"tag":"entry"}],"stats":{}}` + "\n\t"
	result := requireConfigComparison(t, expected, actual)
	if !result.Equivalent || result.ExpectedDigest != result.ActualDigest || len(result.Differences) != 0 || result.Truncated {
		t.Fatal("object key ordering or formatting produced a difference")
	}
	empty := requireConfigComparison(t, `{}`, " { } ")
	if !empty.Equivalent {
		t.Fatal("explicit empty objects must compare equal")
	}
}

func TestCompareConfigPreservesAllFieldsAndArrayOrder(t *testing.T) {
	cases := []struct {
		name, expected, actual, section string
		kind                            ConfigDifferenceKind
	}{
		{"client level lost", `{"inbounds":[{"settings":{"clients":[{"id":"fixture-id","level":3}]}}]}`, `{"inbounds":[{"settings":{"clients":[{"id":"fixture-id"}]}}]}`, "inbounds", ConfigDifferenceRemoved},
		{"large integer", `{"policy":{"quota":9007199254740993}}`, `{"policy":{"quota":9007199254740992}}`, "policy", ConfigDifferenceChanged},
		{"negative large integer", `{"policy":{"quota":-9007199254740993}}`, `{"policy":{"quota":-9007199254740992}}`, "policy", ConfigDifferenceChanged},
		{"numeric spellings", `{"custom":1}`, `{"custom":1.0}`, "other", ConfigDifferenceChanged},
		{"exponent spelling", `{"custom":1e0}`, `{"custom":1E+0}`, "other", ConfigDifferenceChanged},
		{"negative zero", `{"custom":-0}`, `{"custom":0}`, "other", ConfigDifferenceChanged},
		{"number is not string", `{"custom":3}`, `{"custom":"3"}`, "other", ConfigDifferenceChanged},
		{"missing null", `{}`, `{"dns":null}`, "dns", ConfigDifferenceAdded},
		{"missing empty array", `{"inbounds":[]}`, `{}`, "inbounds", ConfigDifferenceRemoved},
		{"null empty array", `{"inbounds":null}`, `{"inbounds":[]}`, "inbounds", ConfigDifferenceChanged},
		{"null empty object", `{"stats":null}`, `{"stats":{}}`, "stats", ConfigDifferenceChanged},
		{"false null", `{"metrics":false}`, `{"metrics":null}`, "metrics", ConfigDifferenceChanged},
		{"false missing", `{"policy":{"statsUserOnline":false}}`, `{"policy":{}}`, "policy", ConfigDifferenceRemoved},
		{"routing reorder", `{"routing":{"rules":[{"outboundTag":"block"},{"outboundTag":"direct"}]}}`, `{"routing":{"rules":[{"outboundTag":"direct"},{"outboundTag":"block"}]}}`, "routing", ConfigDifferenceChanged},
		{"client reorder", `{"inbounds":[{"settings":{"clients":[{"id":"a"},{"id":"b"}]}}]}`, `{"inbounds":[{"settings":{"clients":[{"id":"b"},{"id":"a"}]}}]}`, "inbounds", ConfigDifferenceChanged},
		{"outbound reorder", `{"outbounds":[{"tag":"direct"},{"tag":"block"}]}`, `{"outbounds":[{"tag":"block"},{"tag":"direct"}]}`, "outbounds", ConfigDifferenceChanged},
		{"empty array member added", `{"inbounds":[]}`, `{"inbounds":[null]}`, "inbounds", ConfigDifferenceAdded},
		{"array member removed", `{"inbounds":[{}]}`, `{"inbounds":[]}`, "inbounds", ConfigDifferenceRemoved},
		{"management API changed", `{"api":{"tag":"api"}}`, `{"api":{"tag":"other-api"}}`, "api", ConfigDifferenceChanged},
		{"stats section removed", `{"stats":{}}`, `{}`, "stats", ConfigDifferenceRemoved},
		{"policy stats changed", `{"policy":{"levels":{"0":{"statsUserOnline":true}}}}`, `{"policy":{"levels":{"0":{"statsUserOnline":false}}}}`, "policy", ConfigDifferenceChanged},
		{"unknown field lost", `{"operator-extension":{"private-value":true}}`, `{}`, "other", ConfigDifferenceRemoved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := requireConfigComparison(t, tc.expected, tc.actual)
			if result.Equivalent || result.ExpectedDigest == result.ActualDigest || len(result.Differences) == 0 || result.Truncated {
				t.Fatal("a configuration change was missed")
			}
			for _, difference := range result.Differences {
				if difference.Section != tc.section || difference.Kind != tc.kind {
					t.Fatalf("unexpected fixed difference labels: %+v", difference)
				}
			}
		})
	}
}

func requireConfigComparisonError(t *testing.T, raw []byte, kind ConfigComparisonErrorKind) {
	t.Helper()
	for _, side := range []string{"expected", "actual"} {
		expected, actual := raw, []byte(`{}`)
		if side == "actual" {
			expected, actual = actual, expected
		}
		result, err := CompareConfig(expected, actual)
		var typed *ConfigComparisonError
		if !errors.As(err, &typed) || typed.Side != side || typed.Kind != kind {
			t.Fatalf("validation error = %v, want side %s / kind %s", err, side, kind)
		}
		if !reflect.DeepEqual(result, ConfigComparison{}) {
			t.Fatal("invalid JSON exposed partial comparison evidence")
		}
	}
}

func TestCompareConfigRejectsAmbiguousOrInvalidJSON(t *testing.T) {
	cases := []struct {
		name, raw string
		kind      ConfigComparisonErrorKind
	}{
		{"empty", ``, ConfigComparisonInvalidJSON},
		{"whitespace", " \n\t", ConfigComparisonInvalidJSON},
		{"null root", `null`, ConfigComparisonNotObject},
		{"array root", `[]`, ConfigComparisonNotObject},
		{"boolean root", `false`, ConfigComparisonNotObject},
		{"number root", `42`, ConfigComparisonNotObject},
		{"string root", `"fixture"`, ConfigComparisonNotObject},
		{"two objects", `{} {}`, ConfigComparisonMultipleDocuments},
		{"trailing scalar", `{} true`, ConfigComparisonMultipleDocuments},
		{"trailing array", `{}[]`, ConfigComparisonMultipleDocuments},
		{"trailing malformed bytes", `{} secret-invalid-json`, ConfigComparisonInvalidJSON},
		{"trailing comma", `{"inbounds":[],}`, ConfigComparisonInvalidJSON},
		{"object delimiter", `{"inbounds":[]]`, ConfigComparisonInvalidJSON},
		{"array delimiter", `{"inbounds":[1}}`, ConfigComparisonInvalidJSON},
		{"incomplete array", `{"inbounds":[`, ConfigComparisonInvalidJSON},
		{"invalid numeric prefix", `{"port":01}`, ConfigComparisonInvalidJSON},
		{"non-finite number", `{"port":NaN}`, ConfigComparisonInvalidJSON},
		{"duplicate root key", `{"stats":{},"stats":{}}`, ConfigComparisonDuplicateKey},
		{"duplicate null key", `{"stats":null,"stats":null}`, ConfigComparisonDuplicateKey},
		{"duplicate escaped key", `{"stats":{},"\u0073tats":{}}`, ConfigComparisonDuplicateKey},
		{"duplicate nested key", `{"inbounds":[{"settings":{"clients":[{"level":3,"level":3}]}}]}`, ConfigComparisonDuplicateKey},
		{"duplicate empty key", `{"":false,"":true}`, ConfigComparisonDuplicateKey},
		{"unpaired high surrogate", `{"custom":"\ud800"}`, ConfigComparisonInvalidJSON},
		{"unpaired low surrogate", `{"custom":"\udc00"}`, ConfigComparisonInvalidJSON},
		{"wrong surrogate pair", `{"custom":"\ud800\u0041"}`, ConfigComparisonInvalidJSON},
		{"unpaired key surrogate", `{"\ud800":true}`, ConfigComparisonInvalidJSON},
		{"incomplete unicode escape", `{"custom":"\u12"}`, ConfigComparisonInvalidJSON},
		{"invalid hex escape", `{"custom":"\u00gg"}`, ConfigComparisonInvalidJSON},
		{"invalid string escape", `{"custom":"\q"}`, ConfigComparisonInvalidJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { requireConfigComparisonError(t, []byte(tc.raw), tc.kind) })
	}
	for _, raw := range [][]byte{
		append([]byte(`{"privateKey":"`), 0xff, '"', '}'),
		append([]byte(`{"`), 0xc3, '"', ':', '0', '}'),
		append([]byte(`{}`), 0xff),
	} {
		requireConfigComparisonError(t, raw, ConfigComparisonInvalidUTF8)
	}
}

func TestCompareConfigHandlesValidUnicodeWithoutLoss(t *testing.T) {
	result := requireConfigComparison(t, `{"custom":"\ud83d\ude00","\u0061":1,"escaped":"\\ud800"}`, `{"a":1,"custom":"😀","escaped":"\\ud800"}`)
	if !result.Equivalent {
		t.Fatal("valid surrogate pairs or escaped backslashes changed values")
	}
	if requireConfigComparison(t, `{"custom":"é"}`, `{"custom":"e\u0301"}`).Equivalent {
		t.Fatal("comparison unexpectedly normalised distinct Unicode strings")
	}
	if !requireConfigComparison(t, `{"value":1e9999999999999999999999}`, `{"value":1e9999999999999999999999}`).Equivalent {
		t.Fatal("valid number lexemes were passed through floating-point conversion")
	}
}

func TestCompareConfigSizeAndDepthBoundaries(t *testing.T) {
	prefix, suffix := `{"padding":"`, `"}`
	atLimit := []byte(prefix + strings.Repeat("a", maxConfigComparisonBytes-len(prefix)-len(suffix)) + suffix)
	if len(atLimit) != maxConfigComparisonBytes {
		t.Fatal("incorrect size-boundary fixture")
	}
	if result, err := CompareConfig(atLimit, atLimit); err != nil || !result.Equivalent {
		t.Fatalf("exact size limit was rejected: %v", err)
	}
	requireConfigComparisonError(t, append(bytes.Clone(atLimit), ' '), ConfigComparisonTooLarge)
	for _, container := range []string{"array", "object"} {
		makeNested := func(depth int) string {
			value := `null`
			for i := 1; i < depth; i++ {
				if container == "array" {
					value = "[" + value + "]"
				} else {
					value = `{"nested":` + value + `}`
				}
			}
			return `{"nested":` + value + `}`
		}
		atDepth := makeNested(maxConfigComparisonDepth)
		if !requireConfigComparison(t, atDepth, atDepth).Equivalent {
			t.Fatal("exact depth limit was rejected")
		}
		requireConfigComparisonError(t, []byte(makeNested(maxConfigComparisonDepth+1)), ConfigComparisonTooDeep)
	}
}

func TestCompareConfigDiagnosticsNeverExposeProviderContent(t *testing.T) {
	const key = "arbitrary-secret-key-fixture"
	const email = "secret-person@fixture.invalid"
	const credential = "fixture-private-credential"
	expected := `{"` + key + `":{"` + email + `":"` + credential + `"},"inbounds":[{"settings":{"clients":[{"email":"` + email + `","id":"` + credential + `"}]}}]}`
	result := requireConfigComparison(t, expected, `{}`)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded) + fmt.Sprintf("%+v %#v", result, result)
	for _, secret := range []string{key, email, credential} {
		if strings.Contains(output, secret) {
			t.Fatal("comparison exposed a provider-controlled key or value")
		}
	}
	for _, difference := range result.Differences {
		decoded, err := hex.DecodeString(difference.PathDigest)
		if err != nil || len(decoded) != sha256.Size {
			t.Fatal("difference path was not a SHA-256 digest")
		}
	}
	for _, invalid := range []string{
		`{"` + key + `":null,"` + key + `":true}`,
		`{"privateKey":"` + credential + `","` + email + `":invalid}`,
	} {
		_, err := CompareConfig([]byte(`{}`), []byte(invalid))
		if err == nil {
			t.Fatal("invalid fixture was accepted")
		}
		encoded, marshalErr := json.Marshal(err)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		output := err.Error() + fmt.Sprintf("%+v %#v", err, err) + string(encoded)
		for _, secret := range []string{key, email, credential} {
			if strings.Contains(output, secret) {
				t.Fatal("validation error exposed provider-controlled input")
			}
		}
	}
}

func TestCompareConfigDifferenceLimitsAndStableOrdering(t *testing.T) {
	for _, count := range []int{31, 32, 33, 80} {
		fields := make(map[string]int, count)
		for i := 0; i < count; i++ {
			fields[fmt.Sprintf("private-key-%03d", i)] = i
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		result := requireConfigComparison(t, `{}`, string(encoded))
		if result.Equivalent || len(result.Differences) != min(count, maxConfigComparisonDifferences) || result.Truncated != (count > maxConfigComparisonDifferences) {
			t.Fatalf("incorrect difference cap for %d differences", count)
		}
		var reversed []string
		for i := count - 1; i >= 0; i-- {
			reversed = append(reversed, fmt.Sprintf(`"private-key-%03d":%d`, i, i))
		}
		again := requireConfigComparison(t, `{}`, "{"+strings.Join(reversed, ",")+"}")
		if !reflect.DeepEqual(result, again) {
			t.Fatal("diagnostic ordering or digest depends on object key order")
		}
		for _, difference := range result.Differences {
			if difference.Section != "other" || difference.Kind != ConfigDifferenceAdded {
				t.Fatal("unknown field names escaped the fixed diagnostic labels")
			}
		}
	}
}

func TestCompareConfigHashedPathsDoNotAliasPunctuationOrIndices(t *testing.T) {
	cases := [][2]string{
		{`{"x":{"a/b":1}}`, `{"x":{"a/b":2}}`},
		{`{"x":{"a":{"b":1}}}`, `{"x":{"a":{"b":2}}}`},
		{`{"x":{"a.b":1}}`, `{"x":{"a.b":2}}`},
		{`{"x":{"0":1}}`, `{"x":{"0":2}}`},
		{`{"x":[1]}`, `{"x":[2]}`},
		{`{"x":{"[0]":1}}`, `{"x":{"[0]":2}}`},
		{`{"x":{"":1}}`, `{"x":{"":2}}`},
	}
	seen := map[string]bool{}
	for _, pair := range cases {
		result := requireConfigComparison(t, pair[0], pair[1])
		if len(result.Differences) != 1 {
			t.Fatal("path fixture did not produce exactly one difference")
		}
		path := result.Differences[0].PathDigest
		if seen[path] {
			t.Fatal("distinct paths produced the same digest")
		}
		seen[path] = true
		reverse := requireConfigComparison(t, pair[1], pair[0])
		if reverse.Differences[0].PathDigest != path || reverse.ExpectedDigest != result.ActualDigest || reverse.ActualDigest != result.ExpectedDigest {
			t.Fatal("comparison direction changed a path identity")
		}
	}
}

func TestCompareConfigDoesNotMutateOrRetainArguments(t *testing.T) {
	expected := []byte(`{"inbounds":[{"settings":{"clients":[{"id":"original","level":3}]}}]}`)
	actual := []byte(`{"inbounds":[{"settings":{"clients":[{"id":"changed","level":3}]}}]}`)
	wantBefore, gotBefore := bytes.Clone(expected), bytes.Clone(actual)
	result, err := CompareConfig(expected, actual)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, wantBefore) || !bytes.Equal(actual, gotBefore) {
		t.Fatal("comparison rewrote an input configuration")
	}
	encodedBefore, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	clear(expected)
	clear(actual)
	encodedAfter, err := json.Marshal(result)
	if err != nil || !bytes.Equal(encodedBefore, encodedAfter) {
		t.Fatal("comparison evidence retained mutable argument buffers")
	}
	again, err := CompareConfig(wantBefore, gotBefore)
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatal("comparison is not deterministic")
	}
	result.Differences[0].Section = "caller-changed"
	if again.Differences[0].Section != "inbounds" {
		t.Fatal("comparison results share mutable difference storage")
	}
}

func TestCompareConfigAllKnownSectionsRemainSignificant(t *testing.T) {
	for _, section := range []string{"log", "api", "dns", "routing", "policy", "inbounds", "outbounds", "transport", "stats", "reverse", "fakedns", "observatory", "burstObservatory", "metrics", "browserForwarder"} {
		result := requireConfigComparison(t, `{"`+section+`":{}}`, `{}`)
		if result.Equivalent || len(result.Differences) != 1 || result.Differences[0].Section != section {
			t.Fatal("a known configuration section was ignored or mislabelled")
		}
	}
}

func FuzzCompareConfig(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"stats":{},"policy":{"levels":{"0":{"statsUserOnline":true}}}}`,
		`{"inbounds":[{"settings":{"clients":[{"id":"fixture","level":3}]}}]}`,
		`{"large":9007199254740993}`, `{"duplicate":1,"duplicate":2}`, `{} {}`,
		`{"unicode":"\ud800"}`, `{"unicode":"\ud83d\ude00"}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 16<<10 {
			t.Skip("size boundaries have dedicated unit coverage")
		}
		before := bytes.Clone(input)
		result, err := CompareConfig(input, input)
		if !bytes.Equal(input, before) {
			t.Fatal("input was mutated")
		}
		if err != nil {
			var typed *ConfigComparisonError
			if !errors.As(err, &typed) || !reflect.DeepEqual(result, ConfigComparison{}) {
				t.Fatal("validation exposed an untyped error or partial comparison")
			}
			return
		}
		if !json.Valid(input) || !result.Equivalent || result.ExpectedDigest != result.ActualDigest || len(result.Differences) != 0 || result.Truncated {
			t.Fatal("accepted configuration does not compare equal to itself")
		}
	})
}
