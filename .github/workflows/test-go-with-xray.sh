#!/usr/bin/env bash
set -euo pipefail

: "${CHIRAL_XRAY_BIN:?CHIRAL_XRAY_BIN must name the CI Xray binary}"
: "${XRAY_LOCATION_ASSET:?XRAY_LOCATION_ASSET must contain the CI geo assets}"
xray_dir=$(dirname "$CHIRAL_XRAY_BIN")
export PATH="$xray_dir:$PATH"

for file in "$CHIRAL_XRAY_BIN" \
	"$XRAY_LOCATION_ASSET/geoip.dat" \
	"$XRAY_LOCATION_ASSET/geosite.dat"; do
	if [[ ! -s "$file" ]]; then
		echo "::error::required Xray test input is missing: $file"
		exit 1
	fi
done

# These suites intentionally depend on changing external state or a large
# developer-supplied corpus/archive. Keep them opt-in instead of making the
# ordinary correctness gate flaky or silently expensive.
unset CHIRAL_NETWORK_TESTS
unset CHIRAL_TEST_ARCHIVE CHIRAL_TEST_ARCHIVE_SHA256
unset CHIRAL_ACL4SSR_CORPUS CHIRAL_RENDER_TO

log=$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/chiral-go-test.XXXXXX")
trap 'rm -f -- "$log"' EXIT

# -count=1 prevents the Go test cache from supplying an old PASS produced
# without the binary/assets exported above. JSON gives the assertion below an
# unambiguous package/test/action tuple even if two packages reuse a test name.
go test -json -count=1 -timeout=10m ./... 2>&1 | tee "$log"

# Merely exporting CHIRAL_XRAY_BIN is not enough: a command removed by a future
# Xray release can still make a test call t.Skip. Require every safety-critical
# integration test to have actually passed in this run.
required_tests=(
	github.com/SayukiOvO/chiral/core/internal/template:TestX25519MatchesXrayDerivation
	github.com/SayukiOvO/chiral/core/internal/template:TestMLKEM768MatchesXrayDerivation
	github.com/SayukiOvO/chiral/core/internal/template:TestX25519MatchesTheXrayBinary
	github.com/SayukiOvO/chiral/core/internal/template:TestX25519PublicClampsWhatItIsGiven
	github.com/SayukiOvO/chiral/core/internal/template:TestImportedX25519MatchesTheXrayBinary
	github.com/SayukiOvO/chiral/core/internal/template:TestMLDSA65Derivation
	github.com/SayukiOvO/chiral/core/internal/template:TestTestConfigAcceptsGeoRoutingRules
	github.com/SayukiOvO/chiral/core/internal/profile:TestAssembleProducesValidRealityConfig
	github.com/SayukiOvO/chiral/core/internal/profile:TestApplyRefusesInvalidConfig
	github.com/SayukiOvO/chiral/core/internal/profile:TestConfigWithUsersPassesXrayTest
	github.com/SayukiOvO/chiral/core/internal/profile:TestARealKernelAcceptsGeoEgressRules
	github.com/SayukiOvO/chiral/core/internal/profile:TestRelayedTrafficReallyLeavesThroughTheExit
	github.com/SayukiOvO/chiral/core/internal/profile:TestEgressSendsOnlyMatchedTrafficThroughTheOtherNode
	github.com/SayukiOvO/chiral/core/internal/profile:TestRestrictedTrafficDiesAtTheEntryOfALine
	github.com/SayukiOvO/chiral/agent/internal/xray:TestDataPathProvesTrafficReachesTheInternet
	github.com/SayukiOvO/chiral/agent/internal/xray:TestTheAPIAnswersOnANodeThatCarriesNoTraffic
	github.com/SayukiOvO/chiral/core/internal/external:TestXrayItselfAcceptsEveryConvertedOutbound
)

for required in "${required_tests[@]}"; do
	package=${required%%:*}
	test_name=${required#*:}
	if ! jq -e --arg package "$package" --arg test "$test_name" \
		'select(.Action == "pass" and .Package == $package and .Test == $test)' \
		"$log" >/dev/null; then
		echo "::error::critical real-Xray test did not pass: $package $test_name"
		exit 1
	fi
done

echo "all ${#required_tests[@]} required real-Xray integration tests passed"
