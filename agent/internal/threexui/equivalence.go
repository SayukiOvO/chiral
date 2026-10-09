package threexui

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"unicode/utf8"
)

const (
	maxConfigComparisonBytes       = 4 << 20
	maxConfigComparisonDepth       = 64
	maxConfigComparisonDifferences = 32
)

// ConfigComparison is a fidelity check of two complete configuration objects.
// Digests are lowercase SHA-256 of their canonical JSON: object key order and
// whitespace are ignored, but array order and number spellings are retained.
// No configuration values or provider-controlled keys are returned.
type ConfigComparison struct {
	Equivalent     bool               `json:"equivalent"`
	ExpectedDigest string             `json:"expected_digest"`
	ActualDigest   string             `json:"actual_digest"`
	Differences    []ConfigDifference `json:"differences"`
	Truncated      bool               `json:"truncated"`
}

type ConfigDifferenceKind string

const (
	ConfigDifferenceAdded   ConfigDifferenceKind = "added"
	ConfigDifferenceRemoved ConfigDifferenceKind = "removed"
	ConfigDifferenceChanged ConfigDifferenceKind = "changed"
)

// ConfigDifference identifies a change without exposing its path or values.
// Section is a fixed Xray top-level section name, or "other" for unknown keys.
// PathDigest hashes a domain-separated path with typed, length-prefixed map
// keys and array indices; a key containing punctuation cannot alias a path.
// A digest is not encryption and does not prevent guessing a known path.
type ConfigDifference struct {
	Section    string               `json:"section"`
	Kind       ConfigDifferenceKind `json:"kind"`
	PathDigest string               `json:"path_digest"`
}

type ConfigComparisonErrorKind string

const (
	ConfigComparisonTooLarge          ConfigComparisonErrorKind = "too_large"
	ConfigComparisonInvalidUTF8       ConfigComparisonErrorKind = "invalid_utf8"
	ConfigComparisonInvalidJSON       ConfigComparisonErrorKind = "invalid_json"
	ConfigComparisonDuplicateKey      ConfigComparisonErrorKind = "duplicate_key"
	ConfigComparisonTooDeep           ConfigComparisonErrorKind = "too_deep"
	ConfigComparisonNotObject         ConfigComparisonErrorKind = "not_object"
	ConfigComparisonMultipleDocuments ConfigComparisonErrorKind = "multiple_documents"
)

// ConfigComparisonError contains only fixed labels, never a parser error,
// source offset, input excerpt, duplicate key, or partially decoded config.
// Side is either "expected" or "actual" for errors returned by CompareConfig.
type ConfigComparisonError struct {
	Side string                    `json:"side"`
	Kind ConfigComparisonErrorKind `json:"kind"`
}

func (e *ConfigComparisonError) Error() string {
	side := "configuration"
	if e != nil && (e.Side == "expected" || e.Side == "actual") {
		side = e.Side + " configuration"
	}
	if e == nil {
		return "configuration comparison failed"
	}
	switch e.Kind {
	case ConfigComparisonTooLarge:
		return side + " exceeds the 4 MiB comparison limit"
	case ConfigComparisonInvalidUTF8:
		return side + " contains invalid UTF-8"
	case ConfigComparisonDuplicateKey:
		return side + " contains a duplicate JSON object key"
	case ConfigComparisonTooDeep:
		return side + " exceeds the 64-container comparison depth limit"
	case ConfigComparisonNotObject:
		return side + " must be a JSON object"
	case ConfigComparisonMultipleDocuments:
		return side + " contains multiple JSON documents"
	default:
		return side + " is not valid JSON"
	}
}

// CompareConfig compares complete desired and assembled configuration JSON.
// It never rewrites either input and does not call a provider. Each input must
// be one UTF-8 JSON object of at most 4 MiB and 64 nested containers (including
// the root). Duplicate decoded keys and unpaired UTF-16 escapes are rejected.
//
// All sections participate, including api, stats, policy, and unknown fields.
// No provider-management or default-value exceptions are implicit. Missing,
// null, false, and empty containers remain distinct. Numbers are json.Number,
// so large integers never pass through float64; 1, 1.0, and 1e0 conservatively
// differ. This is not a proof of complete Xray semantic equivalence, syntax
// acceptance by Xray, running-state adoption, or successful data-plane use.
// At most 32 differences are returned, with Truncated set only if more exist.
// A validation error returns a zero comparison, not partial evidence.
func CompareConfig(expected, actual []byte) (ConfigComparison, error) {
	want, err := decodeComparisonConfig(expected, "expected")
	if err != nil {
		return ConfigComparison{}, err
	}
	got, err := decodeComparisonConfig(actual, "actual")
	if err != nil {
		return ConfigComparison{}, err
	}
	// The parser produces only JSON-native values and validated json.Number
	// values. Marshaling cannot fail, but retain a fixed diagnostic if it does.
	wantJSON, err := json.Marshal(want)
	if err != nil {
		return ConfigComparison{}, &ConfigComparisonError{Side: "expected", Kind: ConfigComparisonInvalidJSON}
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		return ConfigComparison{}, &ConfigComparisonError{Side: "actual", Kind: ConfigComparisonInvalidJSON}
	}
	result := ConfigComparison{
		Equivalent:     bytes.Equal(wantJSON, gotJSON),
		ExpectedDigest: comparisonDigest(wantJSON),
		ActualDigest:   comparisonDigest(gotJSON),
		Differences:    []ConfigDifference{},
	}
	if !result.Equivalent {
		root := sha256.Sum256([]byte("chiral-config-comparison-path-v1\x00"))
		result.compareValues(want, got, "other", root, true)
	}
	return result, nil
}

func decodeComparisonConfig(raw []byte, side string) (map[string]any, error) {
	fail := func(kind ConfigComparisonErrorKind) (map[string]any, error) {
		return nil, &ConfigComparisonError{Side: side, Kind: kind}
	}
	if len(raw) > maxConfigComparisonBytes {
		return fail(ConfigComparisonTooLarge)
	}
	if !utf8.Valid(raw) {
		return fail(ConfigComparisonInvalidUTF8)
	}
	if !comparisonUnicodeEscapesValid(raw) {
		return fail(ConfigComparisonInvalidJSON)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, kind := parseComparisonValue(decoder, 0)
	if kind != "" {
		return fail(kind)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fail(ConfigComparisonMultipleDocuments)
		}
		return fail(ConfigComparisonInvalidJSON)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fail(ConfigComparisonNotObject)
	}
	return object, nil
}

func parseComparisonValue(decoder *json.Decoder, depth int) (any, ConfigComparisonErrorKind) {
	token, err := decoder.Token()
	if err != nil {
		return nil, ConfigComparisonInvalidJSON
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return token, ""
	}
	if depth >= maxConfigComparisonDepth {
		return nil, ConfigComparisonTooDeep
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return nil, ConfigComparisonInvalidJSON
			}
			if _, exists := object[key]; exists {
				return nil, ConfigComparisonDuplicateKey
			}
			value, kind := parseComparisonValue(decoder, depth+1)
			if kind != "" {
				return nil, kind
			}
			object[key] = value
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return nil, ConfigComparisonInvalidJSON
		}
		return object, ""
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, kind := parseComparisonValue(decoder, depth+1)
			if kind != "" {
				return nil, kind
			}
			array = append(array, value)
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return nil, ConfigComparisonInvalidJSON
		}
		return array, ""
	default:
		return nil, ConfigComparisonInvalidJSON
	}
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Reject them
// before decoding so distinct invalid strings cannot collapse to one value.
// General syntax validation is still performed by the JSON decoder.
func comparisonUnicodeEscapesValid(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			i++
			if i >= len(raw) || raw[i] != 'u' {
				continue
			}
			unit, ok := comparisonHexUnit(raw, i+1)
			if !ok {
				return false
			}
			i += 4
			if unit >= 0xdc00 && unit <= 0xdfff {
				return false
			}
			if unit >= 0xd800 && unit <= 0xdbff {
				if i+2 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return false
				}
				low, ok := comparisonHexUnit(raw, i+3)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

func comparisonHexUnit(raw []byte, start int) (uint16, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, b := range raw[start : start+4] {
		value <<= 4
		switch {
		case b >= '0' && b <= '9':
			value += uint16(b - '0')
		case b >= 'a' && b <= 'f':
			value += uint16(b-'a') + 10
		case b >= 'A' && b <= 'F':
			value += uint16(b-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func comparisonDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func comparisonKeyPath(parent [32]byte, key string) [32]byte {
	h := sha256.New()
	h.Write(parent[:])
	h.Write([]byte{'k'})
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(key)))
	h.Write(length[:])
	h.Write([]byte(key))
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func comparisonIndexPath(parent [32]byte, index int) [32]byte {
	var encoded [41]byte
	copy(encoded[:32], parent[:])
	encoded[32] = 'i'
	binary.BigEndian.PutUint64(encoded[33:], uint64(index))
	return sha256.Sum256(encoded[:])
}

func comparisonSection(key string) string {
	switch key {
	case "log", "api", "dns", "routing", "policy", "inbounds", "outbounds", "transport", "stats", "reverse", "fakedns", "observatory", "burstObservatory", "metrics", "browserForwarder":
		return key
	default:
		return "other"
	}
}

func (result *ConfigComparison) addDifference(section string, kind ConfigDifferenceKind, path [32]byte) {
	if len(result.Differences) == maxConfigComparisonDifferences {
		result.Truncated = true
		return
	}
	result.Differences = append(result.Differences, ConfigDifference{
		Section: section, Kind: kind, PathDigest: hex.EncodeToString(path[:]),
	})
}

func (result *ConfigComparison) compareValues(want, got any, section string, path [32]byte, root bool) {
	if result.Truncated {
		return
	}
	switch expected := want.(type) {
	case map[string]any:
		if actual, ok := got.(map[string]any); ok {
			keys := make([]string, 0, len(expected)+len(actual))
			for key := range expected {
				keys = append(keys, key)
			}
			for key := range actual {
				if _, exists := expected[key]; !exists {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				if result.Truncated {
					return
				}
				childSection := section
				if root {
					childSection = comparisonSection(key)
				}
				childPath := comparisonKeyPath(path, key)
				wantValue, wantExists := expected[key]
				gotValue, gotExists := actual[key]
				switch {
				case !wantExists:
					result.addDifference(childSection, ConfigDifferenceAdded, childPath)
				case !gotExists:
					result.addDifference(childSection, ConfigDifferenceRemoved, childPath)
				default:
					result.compareValues(wantValue, gotValue, childSection, childPath, false)
				}
			}
			return
		}
	case []any:
		if actual, ok := got.([]any); ok {
			for i := 0; i < max(len(expected), len(actual)) && !result.Truncated; i++ {
				childPath := comparisonIndexPath(path, i)
				switch {
				case i >= len(expected):
					result.addDifference(section, ConfigDifferenceAdded, childPath)
				case i >= len(actual):
					result.addDifference(section, ConfigDifferenceRemoved, childPath)
				default:
					result.compareValues(expected[i], actual[i], section, childPath, false)
				}
			}
			return
		}
	default:
		// Parsed leaves are comparable nil, bool, string, or json.Number.
		// got may be a map/slice, so use a type switch before comparison.
		switch got.(type) {
		case nil, bool, string, json.Number:
			if want == got {
				return
			}
		}
	}
	result.addDifference(section, ConfigDifferenceChanged, path)
}
