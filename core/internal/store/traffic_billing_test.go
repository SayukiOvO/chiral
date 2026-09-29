package store

import (
	"math"
	"testing"
)

func TestTrafficBillingUsesExactDecimalCeiling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		up, down int64
		rate     float64
		want     int64
	}{
		{"decimal-1.1", 37, 63, 1.1, 110},
		{"decimal-0.07", 100, 0, 0.07, 7},
		{"fraction-rounds-up", 1, 2, 0.25, 1},
		{"small-remainder-is-not-an-epsilon", 1, 0, math.Nextafter(1, 2), 2},
		{"large-exact-integer", 1 << 53, 1, 1, 1<<53 + 1},
		{"maximum-exact-integer", math.MaxInt64, 0, 1, math.MaxInt64},
		{"large-decimal-product", 1 << 53, 1, 0.5, 4503599627370497},
		{"smallest-rate", 1, 0, math.SmallestNonzeroFloat64, 1},
		{"free", math.MaxInt64, 0, 0, 0},
		{"empty", 0, 0, math.MaxFloat64, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := billedTrafficBytes(tc.up, tc.down, tc.rate)
			if err != nil || got != tc.want {
				t.Fatalf("billed=%d err=%v, want %d", got, err, tc.want)
			}
		})
	}
}

func TestTrafficBillingRejectsInvalidValuesAndOverflow(t *testing.T) {
	for _, tc := range []struct {
		up, down int64
		rate     float64
	}{
		{-1, 0, 1}, {0, -1, 1}, {math.MaxInt64, 1, 1},
		{1, 0, -1}, {1, 0, math.NaN()}, {1, 0, math.Inf(1)}, {1, 0, math.Inf(-1)},
		{math.MaxInt64, 0, 1.1}, {1, 0, math.MaxFloat64},
	} {
		if _, err := billedTrafficBytes(tc.up, tc.down, tc.rate); err == nil {
			t.Fatalf("accepted invalid billing values: %+v", tc)
		}
	}
}

func TestStatsAndLegacyUseTheSameExactBilling(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			up   int64
			rate float64
			want int64
		}{
			{"decimal", 100, 1.1, 110},
			{"integer", 1<<53 + 1, 1, 1<<53 + 1},
			{"maximum", math.MaxInt64, 1, math.MaxInt64},
		} {
			name := "legacy/" + tc.name
			if acknowledged {
				name = "acknowledged/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				st := testStore(t, storeTestKey)
				f := seedStatsBatch(t, st)
				if err := st.SetNodeTrafficRate(f.node.ID, tc.rate); err != nil {
					t.Fatal(err)
				}
				if acknowledged {
					entries := []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: tc.up}}
					requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, true)
					requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, false)
				} else if err := st.AddCredentialTraffic(f.cred.Email, tc.up, 0); err != nil {
					t.Fatal(err)
				}
				requireStatsUsage(t, st, f.user.ID, tc.want)
				creds, err := st.UserCredentials(f.user.ID)
				if err != nil || len(creds) != 1 || creds[0].UpBytes != tc.up || creds[0].DownBytes != 0 {
					t.Fatal("billing changed the raw credential counters")
				}
			})
		}
	}
}

func TestLegacyBillingOverflowLeavesAllCountersUnchanged(t *testing.T) {
	for _, kind := range []string{"delta-sum", "up-total", "down-total", "usage-total", "rate-product", "negative-rate"} {
		t.Run(kind, func(t *testing.T) {
			st := testStore(t, storeTestKey)
			f := seedStatsBatch(t, st)
			up, down := int64(1), int64(1)
			var err error
			switch kind {
			case "delta-sum":
				up, down = math.MaxInt64, 1
			case "up-total":
				_, err = st.db.Exec(`UPDATE credentials SET up_bytes = ? WHERE id = ?`, math.MaxInt64, f.cred.ID)
			case "down-total":
				_, err = st.db.Exec(`UPDATE credentials SET down_bytes = ? WHERE id = ?`, math.MaxInt64, f.cred.ID)
			case "usage-total":
				_, err = st.db.Exec(`UPDATE users SET used_bytes = ? WHERE id = ?`, math.MaxInt64-1, f.user.ID)
			case "rate-product":
				err = st.SetNodeTrafficRate(f.node.ID, math.MaxFloat64)
			case "negative-rate":
				err = st.SetNodeTrafficRate(f.node.ID, -1)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := statsBatchSnapshot(t, st)
			if err := st.AddCredentialTraffic(f.cred.Email, up, down); err == nil {
				t.Fatal("invalid billing operation succeeded")
			}
			if statsBatchSnapshot(t, st) != before {
				t.Fatal("failed billing operation changed persisted counters")
			}
		})
	}
}
