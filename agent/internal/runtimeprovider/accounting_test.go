package runtimeprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func openTestAccounting(t *testing.T, path, identity string, mode BootstrapMode) *Accounting {
	t.Helper()
	a, err := OpenAccounting(path, identity, mode)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func sampleTestAccounting(t *testing.T, a *Accounting, epoch string, counters ...CumulativeCounter) *AccountingBatch {
	t.Helper()
	batch, err := a.Sample(epoch, counters)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func ackTestAccounting(t *testing.T, a *Accounting, batch *AccountingBatch) {
	t.Helper()
	if batch == nil {
		t.Fatal("expected a pending batch")
	}
	if err := a.Ack(batch.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAccountingBaselineThenDurableRetryAndAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.json")
	a := openTestAccounting(t, path, "panel-A", BaselineOnly)
	if batch := sampleTestAccounting(t, a, "store-generation-1", CumulativeCounter{Key: "user>>>alice", Up: 100, Down: 200}); batch != nil {
		t.Fatalf("adoption billed historical usage: %#v", batch)
	}
	batch := sampleTestAccounting(t, a, "store-generation-1", CumulativeCounter{Key: "user>>>alice", Up: 105, Down: 220})
	if batch.Sequence != 1 || len(batch.ReporterID) != 32 || batch.ID != batch.ReporterID+":1" ||
		!reflect.DeepEqual(batch.Entries, []CounterDelta{{Key: "user>>>alice", Up: 5, Down: 20}}) {
		t.Fatalf("unexpected first batch: %#v", batch)
	}
	// A process restart and a later cumulative sample must both replay exactly
	// the same report until the receiver's durable acknowledgement arrives.
	a = openTestAccounting(t, path, "panel-A", BaselineOnly)
	retry := sampleTestAccounting(t, a, "store-generation-1", CumulativeCounter{Key: "user>>>alice", Up: 110, Down: 225})
	if !reflect.DeepEqual(retry, batch) {
		t.Fatalf("pending delivery changed across restart: %#v != %#v", retry, batch)
	}
	if err := a.Ack("wrong-reporter:1"); err == nil {
		t.Fatal("unrelated acknowledgement cleared a pending report")
	}
	ackTestAccounting(t, a, batch)
	ackTestAccounting(t, a, batch) // A duplicated ack must be harmless.
	a = openTestAccounting(t, path, "panel-A", BaselineOnly)
	next := sampleTestAccounting(t, a, "store-generation-1", CumulativeCounter{Key: "user>>>alice", Up: 110, Down: 225})
	if next.Sequence != 2 || next.ReporterID != batch.ReporterID ||
		!reflect.DeepEqual(next.Entries, []CounterDelta{{Key: "user>>>alice", Up: 5, Down: 5}}) {
		t.Fatalf("unacknowledged sampling lost usage or broke sequence: %#v", next)
	}
}

func TestAccountingBootstrapModesAreExplicit(t *testing.T) {
	for _, mode := range []BootstrapMode{BaselineOnly, CountFromZero} {
		t.Run(map[BootstrapMode]string{BaselineOnly: "baseline", CountFromZero: "count"}[mode], func(t *testing.T) {
			a := openTestAccounting(t, filepath.Join(t.TempDir(), "accounting.json"), "panel", mode)
			batch := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 12, Down: 34})
			if mode == BaselineOnly && batch != nil {
				t.Fatal("baseline mode billed existing usage")
			}
			if mode == CountFromZero && (batch == nil || batch.Entries[0].Up != 12 || batch.Entries[0].Down != 34) {
				t.Fatalf("explicit zero bootstrap lost usage: %#v", batch)
			}
		})
	}
	if _, err := OpenAccounting(filepath.Join(t.TempDir(), "bad.json"), "panel", BootstrapMode(99)); err == nil {
		t.Fatal("unknown bootstrap mode accepted")
	}
}

func TestAccountingRetainsRemovedKeysAndCountsNewKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.json")
	a := openTestAccounting(t, path, "panel", BaselineOnly)
	sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "old", Up: 100, Down: 50})
	if got := sampleTestAccounting(t, a, "epoch"); got != nil {
		t.Fatalf("absent keys caused a bill: %#v", got)
	}
	a = openTestAccounting(t, path, "panel", BaselineOnly)
	batch := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "old", Up: 105, Down: 51}, CumulativeCounter{Key: "new", Up: 2, Down: 3})
	want := []CounterDelta{{Key: "new", Up: 2, Down: 3}, {Key: "old", Up: 5, Down: 1}}
	if !reflect.DeepEqual(batch.Entries, want) || batch.Sequence != 1 {
		t.Fatalf("missing keys were forgotten or sequence skipped: %#v", batch)
	}
}

func TestAccountingCounterResetPreservesObservedPostResetBytes(t *testing.T) {
	a := openTestAccounting(t, filepath.Join(t.TempDir(), "accounting.json"), "panel", BaselineOnly)
	sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 100, Down: 200})
	batch := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 3, Down: 7})
	if !reflect.DeepEqual(batch.Entries, []CounterDelta{{Key: "alice", Up: 3, Down: 7, Reset: true}}) {
		t.Fatalf("reset lost or underflowed usage: %#v", batch)
	}
	ackTestAccounting(t, a, batch)
	next := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 5, Down: 8})
	if !reflect.DeepEqual(next.Entries, []CounterDelta{{Key: "alice", Up: 2, Down: 1}}) {
		t.Fatalf("post-reset watermark was not persisted: %#v", next)
	}
}

func TestAccountingRestoreBaselinesOnlyAfterPendingReportIsAcknowledged(t *testing.T) {
	for _, changeIdentity := range []bool{false, true} {
		t.Run(map[bool]string{false: "epoch", true: "identity"}[changeIdentity], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "accounting.json")
			a := openTestAccounting(t, path, "old-panel", CountFromZero)
			old := sampleTestAccounting(t, a, "old-store", CumulativeCounter{Key: "alice", Up: 100, Down: 200})
			identity, epoch := "old-panel", "new-store"
			if changeIdentity {
				identity, epoch = "new-panel", "old-store"
			}
			a = openTestAccounting(t, path, identity, CountFromZero)
			if replay := sampleTestAccounting(t, a, epoch, CumulativeCounter{Key: "alice", Up: 1000, Down: 2000}); !reflect.DeepEqual(replay, old) {
				t.Fatalf("restore discarded pending usage: %#v", replay)
			}
			ackTestAccounting(t, a, old)
			gap := sampleTestAccounting(t, a, epoch, CumulativeCounter{Key: "alice", Up: 1000, Down: 2000})
			if !gap.Indeterminate || gap.Reason == "" || len(gap.Entries) != 0 || gap.Sequence != 2 || gap.ReporterID != old.ReporterID {
				t.Fatalf("restore was silently rebilled or reporting stream changed: %#v", gap)
			}
			ackTestAccounting(t, a, gap)
			next := sampleTestAccounting(t, a, epoch, CumulativeCounter{Key: "alice", Up: 1001, Down: 2002})
			if next.Sequence != 3 || !reflect.DeepEqual(next.Entries, []CounterDelta{{Key: "alice", Up: 1, Down: 2}}) {
				t.Fatalf("new baseline is incorrect: %#v", next)
			}
		})
	}
}

func TestAccountingRejectsInvalidSnapshotsWithoutAdvancing(t *testing.T) {
	a := openTestAccounting(t, filepath.Join(t.TempDir(), "accounting.json"), "panel", BaselineOnly)
	sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 10, Down: 20})
	for _, counters := range [][]CumulativeCounter{
		{{Key: "alice", Up: math.MaxInt64 + 1}},
		{{Key: "alice", Up: math.MaxInt64, Down: 1}},
		{{Key: "alice", Up: 11}, {Key: "alice", Up: 12}},
		{{Key: ""}}, {{Key: "control\nkey"}},
	} {
		if _, err := a.Sample("epoch", counters); err == nil {
			t.Fatalf("invalid counters accepted: %#v", counters)
		}
	}
	batch := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 11, Down: 22})
	if batch.Sequence != 1 || !reflect.DeepEqual(batch.Entries, []CounterDelta{{Key: "alice", Up: 1, Down: 2}}) {
		t.Fatalf("rejected sample changed accounting: %#v", batch)
	}
}

func TestAccountingPendingBatchCannotBeMutatedByCaller(t *testing.T) {
	a := openTestAccounting(t, filepath.Join(t.TempDir(), "accounting.json"), "panel", CountFromZero)
	batch := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 3})
	batch.ID, batch.Entries[0].Up = "changed", 999
	got := sampleTestAccounting(t, a, "epoch")
	if got.ID == "changed" || got.Entries[0].Up != 3 {
		t.Fatalf("caller modified pending journal state: %#v", got)
	}
}

func TestAccountingWriteFailureRequiresReopenAndPreservesDurableBoundary(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-rename", true: "after-rename"}[afterRename], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "accounting.json")
			a := openTestAccounting(t, path, "panel", BaselineOnly)
			sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 100})
			a.write = func(path string, raw []byte) error {
				if afterRename {
					if err := writeAccountingFile(path, raw); err != nil {
						return err
					}
				}
				return errors.New("simulated durability failure")
			}
			if batch, err := a.Sample("epoch", []CumulativeCounter{{Key: "alice", Up: 105}}); err == nil || batch != nil {
				t.Fatalf("failed persistence exposed billable batch: %#v, %v", batch, err)
			}
			if _, err := a.Sample("epoch", []CumulativeCounter{{Key: "alice", Up: 110}}); err == nil {
				t.Fatal("journal continued across an uncertain disk boundary")
			}
			a = openTestAccounting(t, path, "panel", BaselineOnly)
			batch := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 110})
			wantUp := uint64(10)
			if afterRename {
				wantUp = 5
			}
			if batch.Sequence != 1 || batch.Entries[0].Up != wantUp {
				t.Fatalf("restart lost the durable report boundary: %#v", batch)
			}
		})
	}
}

func TestAccountingRejectsCorruptInsecureAndSymlinkedJournal(t *testing.T) {
	for _, raw := range []string{`{`, `{}`, `{"version":99}`, `null`} {
		path := filepath.Join(t.TempDir(), "accounting.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenAccounting(path, "panel", BaselineOnly); err == nil {
			t.Fatalf("damaged journal silently reset: %q", raw)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "accounting.json")
	openTestAccounting(t, path, "panel", BaselineOnly)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal permissions = %v, error %v", info, err)
	}
	link := filepath.Join(dir, "journal-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAccounting(link, "panel", BaselineOnly); err == nil {
		t.Fatal("symlinked journal accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAccounting(path, "panel", BaselineOnly); err == nil {
		t.Fatal("world-readable journal accepted")
	}
}

func TestAccountingRejectsTamperedPendingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.json")
	a := openTestAccounting(t, path, "panel", CountFromZero)
	sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "alice", Up: 3})
	state := cloneAccountingState(a.state)
	state.Pending.Sequence++
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAccounting(path, "panel", CountFromZero); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("inconsistent report sequence accepted: %v", err)
	}
}

func largeAccountingSnapshot(count int, up, down uint64) []CumulativeCounter {
	counters := make([]CumulativeCounter, count)
	for i := range counters {
		// Reverse input order to exercise deterministic fragment boundaries.
		counters[i] = CumulativeCounter{Key: fmt.Sprintf("user>>>subscriber-%06d", count-i), Up: up, Down: down}
	}
	return counters
}

func drainAccountingQueue(t *testing.T, a *Accounting) (up, down uint64, count int) {
	t.Helper()
	for a.state.Pending != nil {
		batch := sampleTestAccounting(t, a, "ignored-while-queued")
		if len(batch.Entries) > chiralv1.MaxStatsBatchEntries {
			t.Fatalf("undeliverable fragment has %d entries", len(batch.Entries))
		}
		for _, entry := range batch.Entries {
			up += entry.Up
			down += entry.Down
			count++
		}
		ackTestAccounting(t, a, batch)
	}
	return up, down, count
}

func TestAccountingLargeSnapshotHasDurableOrderedFragments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.json")
	a := openTestAccounting(t, path, "panel", CountFromZero)
	count := 2*chiralv1.MaxStatsBatchEntries + 3
	original := largeAccountingSnapshot(count, 100, 200)
	future := largeAccountingSnapshot(count, 110, 220)
	first := sampleTestAccounting(t, a, "epoch", original...)
	if first.Sequence != 1 || len(first.Entries) != chiralv1.MaxStatsBatchEntries || a.state.Sequence != 3 || len(a.state.Queued) != 2 {
		t.Fatalf("snapshot was not split durably: first=%d entries=%d reserved=%d queued=%d", first.Sequence, len(first.Entries), a.state.Sequence, len(a.state.Queued))
	}
	if err := a.Ack(a.state.Queued[1].ID); err == nil {
		t.Fatal("acknowledging an unexposed future fragment discarded earlier usage")
	}
	seen := make(map[string]bool)
	var up, down uint64
	sequence := uint64(1)
	for a.state.Pending != nil {
		want := cloneAccountingBatch(a.state.Pending)
		a = openTestAccounting(t, path, "panel", CountFromZero)
		batch := sampleTestAccounting(t, a, "newer-epoch", future...)
		if !reflect.DeepEqual(batch, want) || batch.Sequence != sequence || batch.ReporterID != first.ReporterID {
			t.Fatalf("fragment changed after reopen: sequence=%d want=%d", batch.Sequence, sequence)
		}
		if a.state.SnapshotEpoch != "epoch" {
			t.Fatal("a newer snapshot was consumed before all old fragments were acknowledged")
		}
		for _, entry := range batch.Entries {
			if seen[entry.Key] || entry.Up != 100 || entry.Down != 200 {
				t.Fatalf("fragment overlaps or mixes snapshots: %+v", entry)
			}
			seen[entry.Key] = true
			up += entry.Up
			down += entry.Down
		}
		ackTestAccounting(t, a, batch)
		ackTestAccounting(t, a, batch) // A repeated ack cannot skip the next fragment.
		sequence++
	}
	if len(seen) != count || up != uint64(count)*100 || down != uint64(count)*200 {
		t.Fatalf("fragmentation lost or duplicated usage: keys=%d up=%d down=%d", len(seen), up, down)
	}
	a = openTestAccounting(t, path, "panel", CountFromZero)
	if batch := sampleTestAccounting(t, a, "epoch", original...); batch != nil {
		t.Fatal("the committed snapshot was billed again after its queue drained")
	}
	next := sampleTestAccounting(t, a, "epoch", future...)
	if next.Sequence != 4 {
		t.Fatalf("new snapshot reused reserved sequences: %d", next.Sequence)
	}
	up, down, gotCount := drainAccountingQueue(t, a)
	if gotCount != count || up != uint64(count)*10 || down != uint64(count)*20 {
		t.Fatalf("post-queue sample lost intervening growth: keys=%d up=%d down=%d", gotCount, up, down)
	}
}

func TestAccountingRestoreWaitsForEveryQueuedFragment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.json")
	a := openTestAccounting(t, path, "old-panel", CountFromZero)
	count := chiralv1.MaxStatsBatchEntries + 1
	sampleTestAccounting(t, a, "old-store", largeAccountingSnapshot(count, 1, 2)...)
	a = openTestAccounting(t, path, "new-panel", CountFromZero)
	for sequence := uint64(1); sequence <= 2; sequence++ {
		batch := sampleTestAccounting(t, a, "new-store", largeAccountingSnapshot(count, 100, 200)...)
		if batch.Sequence != sequence || batch.Epoch != "old-store" || batch.Indeterminate {
			t.Fatalf("restore replaced queued traffic: %#v", batch)
		}
		ackTestAccounting(t, a, batch)
		a = openTestAccounting(t, path, "new-panel", CountFromZero)
	}
	gap := sampleTestAccounting(t, a, "new-store", largeAccountingSnapshot(count, 100, 200)...)
	if gap.Sequence != 3 || !gap.Indeterminate || len(gap.Entries) != 0 || len(a.state.Queued) != 0 {
		t.Fatalf("restore did not establish an explicit new baseline after draining: %#v", gap)
	}
	ackTestAccounting(t, a, gap)
	sampleTestAccounting(t, a, "new-store", largeAccountingSnapshot(count, 101, 202)...)
	up, down, gotCount := drainAccountingQueue(t, a)
	if gotCount != count || up != uint64(count) || down != uint64(count)*2 {
		t.Fatalf("new-store delta used the old watermark: keys=%d up=%d down=%d", gotCount, up, down)
	}
}

func TestAccountingFragmentedSampleWriteFailurePreservesAllDeltas(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-rename=%v", afterRename), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "accounting.json")
			a := openTestAccounting(t, path, "panel", BaselineOnly)
			count := chiralv1.MaxStatsBatchEntries + 1
			sampleTestAccounting(t, a, "epoch", largeAccountingSnapshot(count, 100, 200)...)
			a.write = func(path string, raw []byte) error {
				if afterRename {
					if err := writeAccountingFile(path, raw); err != nil {
						return err
					}
				}
				return errors.New("simulated fragmented snapshot durability failure")
			}
			if batch, err := a.Sample("epoch", largeAccountingSnapshot(count, 101, 202)); err == nil || batch != nil {
				t.Fatalf("failed durable write exposed a fragment: %#v err=%v", batch, err)
			}
			if _, err := a.Sample("epoch", largeAccountingSnapshot(count, 102, 204)); err == nil {
				t.Fatal("sampling continued after uncertain persistence")
			}
			a = openTestAccounting(t, path, "panel", BaselineOnly)
			sampleTestAccounting(t, a, "epoch", largeAccountingSnapshot(count, 102, 204)...)
			up, down, _ := drainAccountingQueue(t, a)
			sampleTestAccounting(t, a, "epoch", largeAccountingSnapshot(count, 102, 204)...)
			remainingUp, remainingDown, _ := drainAccountingQueue(t, a)
			if up+remainingUp != uint64(count)*2 || down+remainingDown != uint64(count)*4 {
				t.Fatalf("durability failure lost or duplicated fragments: up=%d down=%d", up+remainingUp, down+remainingDown)
			}
		})
	}
}

func TestAccountingFragmentAcknowledgementWriteFailureIsRecoverable(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-rename=%v", afterRename), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "accounting.json")
			a := openTestAccounting(t, path, "panel", CountFromZero)
			first := sampleTestAccounting(t, a, "epoch", largeAccountingSnapshot(chiralv1.MaxStatsBatchEntries+1, 1, 2)...)
			second := cloneAccountingBatch(a.state.Queued[0])
			a.write = func(path string, raw []byte) error {
				if afterRename {
					if err := writeAccountingFile(path, raw); err != nil {
						return err
					}
				}
				return errors.New("simulated acknowledgement durability failure")
			}
			if err := a.Ack(first.ID); err == nil {
				t.Fatal("acknowledgement persistence failure was ignored")
			}
			if _, err := a.Sample("epoch", nil); err == nil {
				t.Fatal("an uncertain ack boundary exposed the next fragment")
			}
			a = openTestAccounting(t, path, "panel", CountFromZero)
			want := first
			if afterRename {
				want = second
			}
			if got := sampleTestAccounting(t, a, "epoch"); !reflect.DeepEqual(got, want) {
				t.Fatal("restart changed the durable unacknowledged fragment")
			}
			ackTestAccounting(t, a, first)
			if got := sampleTestAccounting(t, a, "epoch"); !reflect.DeepEqual(got, second) {
				t.Fatal("retrying the first acknowledgement skipped or changed the second fragment")
			}
			ackTestAccounting(t, a, second)
			if a.state.Pending != nil || len(a.state.Queued) != 0 {
				t.Fatal("completed queue did not drain")
			}
		})
	}
}

func TestAccountingValidatesWireKeysBeforePersistingWatermarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.json")
	a := openTestAccounting(t, path, "panel", CountFromZero)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		strings.Repeat("x", chiralv1.MaxStatsNameBytes+1),
		"user>>>" + strings.Repeat("x", chiralv1.MaxStatsNameBytes+1),
		"user>>>", "machine>>>name", "user>>>bad\nname", "user>>> name", "user>>>\xff", "generic\xff",
	} {
		if _, err := a.Sample("epoch", []CumulativeCounter{{Key: key, Up: 1}}); err == nil {
			t.Fatalf("undeliverable key was accepted: %q", key)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) || a.state.Sequence != 0 || len(a.state.Counters) != 0 {
			t.Fatalf("rejected key changed durable accounting: %q err=%v", key, err)
		}
	}
	// The scope prefix is not part of the wire name's byte limit. Unicode is
	// counted by bytes, and an unscoped key remains valid for generic providers.
	keys := []string{
		"outbound>>>" + strings.Repeat("x", chiralv1.MaxStatsNameBytes),
		"user>>>" + strings.Repeat("界", chiralv1.MaxStatsNameBytes/3),
		strings.Repeat("g", chiralv1.MaxStatsNameBytes),
	}
	var counters []CumulativeCounter
	for _, key := range keys {
		counters = append(counters, CumulativeCounter{Key: key, Up: 1})
	}
	batch := sampleTestAccounting(t, a, "epoch", counters...)
	if len(batch.Entries) != len(keys) {
		t.Fatal("legal maximum-length keys were discarded")
	}
	openTestAccounting(t, path, "panel", CountFromZero)
}

func TestAccountingRejectsDamagedQueuesWithoutRewritingThem(t *testing.T) {
	a := openTestAccounting(t, filepath.Join(t.TempDir(), "source.json"), "panel", CountFromZero)
	sampleTestAccounting(t, a, "epoch", largeAccountingSnapshot(chiralv1.MaxStatsBatchEntries+1, 1, 2)...)
	for name, mutate := range map[string]func(*accountingState){
		"sequence-gap":     func(s *accountingState) { s.Queued[0].Sequence++ },
		"missing-head":     func(s *accountingState) { s.Pending = nil },
		"missing-tail":     func(s *accountingState) { s.Queued = nil },
		"nil-fragment":     func(s *accountingState) { s.Queued[0] = nil },
		"foreign-reporter": func(s *accountingState) { s.Queued[0].ReporterID = strings.Repeat("a", 32) },
		"duplicate-key":    func(s *accountingState) { s.Queued[0].Entries[0] = s.Pending.Entries[0] },
		"legacy-oversized-unacknowledged": func(s *accountingState) {
			s.Pending.Entries = append(s.Pending.Entries, s.Queued[0].Entries...)
			s.Sequence, s.Queued = s.Pending.Sequence, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := cloneAccountingState(a.state)
			mutate(&state)
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "accounting.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenAccounting(path, "panel", CountFromZero); err == nil {
				t.Fatal("undeliverable or inconsistent persisted queue was accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("recovery changed unacknowledged data or silently reset watermarks")
			}
		})
	}
}

func TestAccountingReservesAllFragmentSequencesBeforeAdvancing(t *testing.T) {
	a := openTestAccounting(t, filepath.Join(t.TempDir(), "accounting.json"), "panel", CountFromZero)
	a.state.Sequence = math.MaxInt64 - 1
	a.state.Initialized, a.state.SnapshotEpoch = true, "epoch"
	before := cloneAccountingState(a.state)
	if batch, err := a.Sample("epoch", largeAccountingSnapshot(chiralv1.MaxStatsBatchEntries+1, 1, 2)); err == nil || batch != nil {
		t.Fatalf("sequence exhaustion exposed a partial snapshot: %#v err=%v", batch, err)
	}
	if !reflect.DeepEqual(a.state, before) {
		t.Fatal("partial sequence reservation advanced the journal")
	}
	batch := sampleTestAccounting(t, a, "epoch", CumulativeCounter{Key: "user>>>last", Up: 1})
	if batch.Sequence != math.MaxInt64 {
		t.Fatalf("last valid sequence not available after rejected fragmentation: %d", batch.Sequence)
	}
}
