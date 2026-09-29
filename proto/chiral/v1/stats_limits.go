package chiralv1

import (
	"encoding/hex"
	"math"
	"strings"
	"unicode/utf8"
)

// These application limits are shared by the durable Agent journal and Core.
// At the maximum name length, a batch remains well below gRPC's 4 MiB default
// receive limit, including protobuf field overhead and the delivery identity.
const (
	MaxStatsBatchEntries = 4096
	MaxStatsNameBytes    = 512
)

func ValidStatsIdentity(reporterID string, sequence uint64) bool {
	if len(reporterID) != 32 || reporterID != strings.ToLower(reporterID) || sequence == 0 || sequence > math.MaxInt64 {
		return false
	}
	_, err := hex.DecodeString(reporterID)
	return err == nil
}

func ValidStatsName(name string) bool {
	if name == "" || len(name) > MaxStatsNameBytes || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// SplitStatsKey decodes the provider journal's scoped wire identity. A name
// may contain the delimiter; only the first delimiter separates its scope.
func SplitStatsKey(key string) (scope, name string, ok bool) {
	scope, name, ok = strings.Cut(key, ">>>")
	if !ok || (scope != "user" && scope != "inbound" && scope != "outbound") || !ValidStatsName(name) {
		return "", "", false
	}
	return scope, name, true
}
