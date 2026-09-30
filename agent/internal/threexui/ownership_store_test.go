//go:build linux || darwin

package threexui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func ownershipStoreFixtureBinding() OwnershipStoreBinding {
	return OwnershipStoreBinding{EndpointHash: strings.Repeat("a", 64), PanelGUID: "fixture-panel-guid"}
}

func ownershipStoreFixtureCheckpoint(seed string) OwnershipCheckpoint {
	return OwnershipCheckpoint{
		Keys: OwnedKeys{
			InboundTags: []string{"z-inbound", "a-inbound"},
			Clients: []OwnedClientKey{
				{Email: "z@example.test", InboundTag: "z-inbound"},
				{Email: "a@example.test", InboundTag: "a-inbound"},
			},
		},
		TemplateDigest: updateDigest("template-" + seed),
		ConfigDigest:   updateDigest("config-" + seed),
	}
}

func createOwnershipStoreFixture(t *testing.T) (string, *OwnershipStore) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ownership")
	initial := OwnershipCheckpoint{TemplateDigest: updateDigest("initial-template"), ConfigDigest: updateDigest("initial-config")}
	store, err := CreateOwnershipStore(dir, ownershipStoreFixtureBinding(), initial)
	if err != nil {
		t.Fatal(err)
	}
	return dir, store
}

func TestOwnershipStoreCreateRestartAndScopeAreStable(t *testing.T) {
	dir, store := createOwnershipStoreFixture(t)
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("ownership directory is not private")
	}
	info, err = os.Stat(filepath.Join(dir, ownershipStoreFilename))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("ownership state file is not private")
	}
	binding, err := store.Binding()
	if err != nil || binding != ownershipStoreFixtureBinding() {
		t.Fatal("binding was not retained")
	}
	scope, err := store.Scope()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := scope.InboundMarker("fixture-inbound")
	if err != nil {
		t.Fatal(err)
	}
	// A returned scope is independent of the store's retained secret.
	scope.key[0] ^= 0xff
	scope.ownerID = strings.Repeat("0", 32)
	secondScope, err := store.Scope()
	if err != nil {
		t.Fatal(err)
	}
	secondMarker, err := secondScope.InboundMarker("fixture-inbound")
	if err != nil || marker != secondMarker {
		t.Fatal("mutating a returned scope changed retained identity")
	}
	if err := store.Close(); err != nil || store.Close() != nil {
		t.Fatal("Close is not idempotent")
	}
	store, err = OpenOwnershipStore(dir, ownershipStoreFixtureBinding())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopenedScope, err := store.Scope()
	if err != nil {
		t.Fatal(err)
	}
	reopenedMarker, err := reopenedScope.InboundMarker("fixture-inbound")
	if err != nil || marker != reopenedMarker {
		t.Fatal("owner identity or MAC key changed across restart")
	}
}

func TestOwnershipStorePrepareRequiresTerminalAcknowledgementAndExactReadback(t *testing.T) {
	dir, store := createOwnershipStoreFixture(t)
	after := ownershipStoreFixtureCheckpoint("after")
	rawOperationID := "private-raw-operation-id-must-not-be-persisted"
	requestHash := updateDigest("request")
	if err := store.Prepare(rawOperationID, 0, requestHash, after); err != nil {
		t.Fatal(err)
	}
	// Mutating every caller-owned slice after Prepare must not change state.
	after.Keys.InboundTags[0] = "mutated"
	after.Keys.Clients[0].Email = "mutated@example.test"
	pending, err := store.Pending()
	if err != nil || pending == nil || pending.OperationIDHash != updateDigest(rawOperationID) || pending.RequestHash != requestHash {
		t.Fatal("pending intent was not retained without raw operation ID")
	}
	wantAfter := ownershipStoreFixtureCheckpoint("after")
	wantAfter, _ = normalizeOwnershipCheckpoint(wantAfter)
	if !reflect.DeepEqual(pending.After, wantAfter) {
		t.Fatal("pending checkpoint was not normalized and copied")
	}
	pending.After.Keys.InboundTags[0] = "changed-return-value"
	pending, _ = store.Pending()
	if !reflect.DeepEqual(pending.After, wantAfter) {
		t.Fatal("Pending returned shared slices")
	}
	raw, err := os.ReadFile(filepath.Join(dir, ownershipStoreFilename))
	if err != nil || bytes.Contains(raw, []byte(rawOperationID)) {
		t.Fatal("raw operation ID was persisted")
	}
	if err := store.Complete(rawOperationID, false, wantAfter); err == nil {
		t.Fatal("missing terminal acknowledgement completed an intent")
	}
	if retained, _ := store.Pending(); retained == nil {
		t.Fatal("missing acknowledgement cleared the write barrier")
	}
	wrong := cloneOwnershipCheckpoint(wantAfter)
	wrong.ConfigDigest = updateDigest("different")
	if err := store.Complete(rawOperationID, true, wrong); err == nil {
		t.Fatal("non-exact readback completed an intent")
	}
	if err := store.Complete("other-operation", true, wantAfter); err == nil {
		t.Fatal("different operation completed an intent")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenOwnershipStore(dir, ownershipStoreFixtureBinding())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Prepare("blocked", 0, updateDigest("blocked"), wantAfter); err == nil {
		t.Fatal("restart with an unresolved intent accepted a new write")
	}
	if err := store.Complete(rawOperationID, true, wantAfter); err != nil {
		t.Fatal(err)
	}
	checkpoint, generation, err := store.Checkpoint()
	if err != nil || generation != 1 || !reflect.DeepEqual(checkpoint, wantAfter) {
		t.Fatal("completion did not atomically advance the checkpoint")
	}
	checkpoint.Keys.InboundTags[0] = "changed-return-value"
	checkpointAgain, _, _ := store.Checkpoint()
	if !reflect.DeepEqual(checkpointAgain, wantAfter) {
		t.Fatal("Checkpoint returned shared slices")
	}
	if intent, err := store.Pending(); err != nil || intent != nil {
		t.Fatal("completed intent remains pending")
	}
	if err := store.Prepare(rawOperationID, 1, updateDigest("another-request"), wantAfter); err == nil {
		t.Fatal("completed operation ID was replayed")
	}
	if err := store.Prepare("new-operation", 0, updateDigest("request"), wantAfter); err == nil {
		t.Fatal("stale generation was accepted")
	}
}

func TestOwnershipStoreCreateIsOneShotAndOpenNeverRecreates(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "ownership")
	initial := OwnershipCheckpoint{TemplateDigest: updateDigest("template"), ConfigDigest: updateDigest("config")}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateOwnershipStore(dir, ownershipStoreFixtureBinding(), initial); err == nil {
		t.Fatal("Create accepted an existing directory")
	}
	if _, err := OpenOwnershipStore(filepath.Join(parent, "missing"), ownershipStoreFixtureBinding()); err == nil {
		t.Fatal("Open created a missing directory or state")
	}
	newDir := filepath.Join(parent, "new")
	store, err := CreateOwnershipStore(newDir, ownershipStoreFixtureBinding(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(newDir, ownershipStoreFilename)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOwnershipStore(newDir, ownershipStoreFixtureBinding()); err == nil {
		t.Fatal("Open accepted missing state")
	}
	if _, err := CreateOwnershipStore(newDir, ownershipStoreFixtureBinding(), initial); err == nil {
		t.Fatal("Create regenerated state in a tombstoned directory")
	}
}

func TestOwnershipStoreRejectsInvalidCreationInputs(t *testing.T) {
	validInitial := OwnershipCheckpoint{TemplateDigest: updateDigest("template"), ConfigDigest: updateDigest("config")}
	cases := []struct {
		name       string
		binding    OwnershipStoreBinding
		checkpoint OwnershipCheckpoint
	}{
		{"endpoint", OwnershipStoreBinding{EndpointHash: "bad", PanelGUID: "panel"}, validInitial},
		{"panel", OwnershipStoreBinding{EndpointHash: strings.Repeat("a", 64), PanelGUID: " panel"}, validInitial},
		{"digest", ownershipStoreFixtureBinding(), OwnershipCheckpoint{TemplateDigest: "bad", ConfigDigest: updateDigest("config")}},
		{"nonempty", ownershipStoreFixtureBinding(), ownershipStoreFixtureCheckpoint("nonempty")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "ownership")
			if _, err := CreateOwnershipStore(dir, tc.binding, tc.checkpoint); err == nil {
				t.Fatal("invalid creation input was accepted")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input created a directory")
			}
		})
	}
}

func TestOwnershipStoreLockAndBindingMismatchFailClosed(t *testing.T) {
	dir, store := createOwnershipStoreFixture(t)
	if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
		t.Fatal("second writer acquired the lifetime lock")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	changed := ownershipStoreFixtureBinding()
	changed.PanelGUID = "other-panel"
	if _, err := OpenOwnershipStore(dir, changed); err == nil {
		t.Fatal("changed panel identity opened private state")
	}
	changed = ownershipStoreFixtureBinding()
	changed.EndpointHash = strings.Repeat("b", 64)
	if _, err := OpenOwnershipStore(dir, changed); err == nil {
		t.Fatal("changed endpoint opened private state")
	}
	reopened, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipStoreRejectsAmbiguousOrIncompleteState(t *testing.T) {
	mutations := map[string]func([]byte) []byte{
		"duplicate": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"schema":1,`), []byte(`{"schema":1,"schema":1,`), 1)
		},
		"case_alias_duplicate": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"schema":1,`), []byte(`{"schema":1,"Schema":1,`), 1)
		},
		"unknown": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"schema":1,`), []byte(`{"schema":1,"unknown":true,`), 1)
		},
		"trailing":     func(raw []byte) []byte { return append(raw, []byte(` {}`)...) },
		"invalid_utf8": func(raw []byte) []byte { return append(raw, 0xff) },
		"missing_zero_field": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"generation":0,`), nil, 1)
		},
		"case_alias": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"generation":0`), []byte(`"Generation":0`), 1)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			dir, store := createOwnershipStoreFixture(t)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ownershipStoreFilename)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, mutate(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
				t.Fatal("ambiguous or incomplete state was accepted")
			}
		})
	}
}

func TestOwnershipStoreRejectsNestedAliasesAcrossRetainedState(t *testing.T) {
	type fixtureStep func(*testing.T, *OwnershipStore)
	cases := []struct {
		name string
		step fixtureStep
		from string
		to   string
	}{
		{"binding", nil, `"endpoint_hash"`, `"ENDPOINT_HASH"`},
		{"keys_object", nil, `"keys"`, `"Keys"`},
		{"checkpoint_keys", nil, `"inbound_tags"`, `"INBOUND_TAGS"`},
		{"pending", func(t *testing.T, store *OwnershipStore) {
			if err := store.Prepare("operation", 0, updateDigest("request"), ownershipStoreFixtureCheckpoint("after")); err != nil {
				t.Fatal(err)
			}
		}, `"expected_generation"`, `"EXPECTED_GENERATION"`},
		{"pending_client_key", func(t *testing.T, store *OwnershipStore) {
			if err := store.Prepare("operation", 0, updateDigest("request"), ownershipStoreFixtureCheckpoint("after")); err != nil {
				t.Fatal(err)
			}
		}, `"inbound_tag"`, `"INBOUND_TAG"`},
		{"pending_client_email", func(t *testing.T, store *OwnershipStore) {
			if err := store.Prepare("operation", 0, updateDigest("request"), ownershipStoreFixtureCheckpoint("after")); err != nil {
				t.Fatal(err)
			}
		}, `"email"`, `"Email"`},
		{"history", func(t *testing.T, store *OwnershipStore) {
			after, err := normalizeOwnershipCheckpoint(ownershipStoreFixtureCheckpoint("after"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Prepare("operation", 0, updateDigest("request"), after); err != nil {
				t.Fatal(err)
			}
			if err := store.Complete("operation", true, after); err != nil {
				t.Fatal(err)
			}
		}, `"after_digest"`, `"AFTER_DIGEST"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, store := createOwnershipStoreFixture(t)
			if tc.step != nil {
				tc.step(t, store)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ownershipStoreFilename)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := bytes.Replace(raw, []byte(tc.from), []byte(tc.to), 1)
			if bytes.Equal(changed, raw) {
				t.Fatal("fixture did not locate nested field")
			}
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
				t.Fatal("noncanonical nested field alias was accepted")
			}
		})
	}
}

func TestOwnershipStoreRejectsUnsafeFilesystemObjects(t *testing.T) {
	t.Run("state_mode", func(t *testing.T) {
		dir, store := createOwnershipStoreFixture(t)
		store.Close()
		if err := os.Chmod(filepath.Join(dir, ownershipStoreFilename), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
			t.Fatal("state with broad permissions was accepted")
		}
	})
	t.Run("directory_mode", func(t *testing.T) {
		dir, store := createOwnershipStoreFixture(t)
		store.Close()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
			t.Fatal("directory with broad permissions was accepted")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		dir, store := createOwnershipStoreFixture(t)
		store.Close()
		path := filepath.Join(dir, ownershipStoreFilename)
		if err := os.Link(path, filepath.Join(dir, "second-link")); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
			t.Fatal("hard-linked state was accepted")
		}
	})
	t.Run("state_symlink", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "ownership")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(parent, "target")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, ownershipStoreFilename)); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
			t.Fatal("state symlink was accepted")
		}
	})
	t.Run("state_fifo", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "ownership")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(dir, ownershipStoreFilename), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
			t.Fatal("FIFO state was accepted")
		}
	})
	t.Run("directory_symlink", func(t *testing.T) {
		dir, store := createOwnershipStoreFixture(t)
		store.Close()
		link := filepath.Join(t.TempDir(), "ownership-link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOwnershipStore(link, ownershipStoreFixtureBinding()); err == nil {
			t.Fatal("directory symlink was accepted")
		}
	})
}

func TestOwnershipStorePoisonsOnExternalReplacementOrInPlaceChange(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "in_place"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			dir, store := createOwnershipStoreFixture(t)
			defer store.Close()
			path := filepath.Join(dir, ownershipStoreFilename)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if replacement {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				raw = append(raw, '\n')
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := store.Checkpoint(); err == nil {
				t.Fatal("externally changed state remained usable")
			}
			if _, err := store.Binding(); err == nil {
				t.Fatal("changed state did not poison the live object")
			}
		})
	}
}

func TestOwnershipStorePoisonsAfterPersistenceFailureAndRetainsUncertainWrite(t *testing.T) {
	t.Run("before_write", func(t *testing.T) {
		_, store := createOwnershipStoreFixture(t)
		defer store.Close()
		store.writeFile = func(*os.File, ownershipFileIdentity, string, []byte) (ownershipFileIdentity, error) {
			return ownershipFileIdentity{}, errors.New("fixture persistence failure")
		}
		if err := store.Prepare("operation", 0, updateDigest("request"), ownershipStoreFixtureCheckpoint("after")); err == nil {
			t.Fatal("persistence failure was accepted")
		}
		if _, err := store.Pending(); err == nil {
			t.Fatal("persistence failure did not poison the object")
		}
	})
	t.Run("after_replace", func(t *testing.T) {
		dir, store := createOwnershipStoreFixture(t)
		original := store.writeFile
		store.writeFile = func(dirFile *os.File, identity ownershipFileIdentity, hash string, data []byte) (ownershipFileIdentity, error) {
			if _, err := original(dirFile, identity, hash, data); err != nil {
				return ownershipFileIdentity{}, err
			}
			return ownershipFileIdentity{}, errors.New("fixture lost completion")
		}
		after := ownershipStoreFixtureCheckpoint("after")
		if err := store.Prepare("operation", 0, updateDigest("request"), after); err == nil {
			t.Fatal("uncertain persistence was accepted")
		}
		if _, err := store.Pending(); err == nil {
			t.Fatal("uncertain persistence did not poison the object")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		pending, err := reopened.Pending()
		if err != nil || pending == nil || pending.OperationIDHash != updateDigest("operation") {
			t.Fatal("durable uncertain intent was not recovered")
		}
	})
}

func TestOwnershipStoreRetainsBoundedReplayHistory(t *testing.T) {
	dir, store := createOwnershipStoreFixture(t)
	after, err := normalizeOwnershipCheckpoint(ownershipStoreFixtureCheckpoint("same"))
	if err != nil {
		t.Fatal(err)
	}
	for generation := 0; generation < maxOwnershipHistory; generation++ {
		operation := fmt.Sprintf("operation-%03d", generation)
		if err := store.Prepare(operation, uint64(generation), updateDigest(fmt.Sprintf("request-%03d", generation)), after); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(operation, true, after); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Prepare("one-too-many", maxOwnershipHistory, updateDigest("request"), after); err == nil {
		t.Fatal("full retained history silently evicted an entry")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, generation, err := reopened.Checkpoint()
	if err != nil || generation != maxOwnershipHistory {
		t.Fatal("bounded history did not survive restart")
	}
}

func TestOwnershipStoreRejectsCorruptHistory(t *testing.T) {
	dir, store := createOwnershipStoreFixture(t)
	after, _ := normalizeOwnershipCheckpoint(ownershipStoreFixtureCheckpoint("after"))
	if err := store.Prepare("operation", 0, updateDigest("request"), after); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("operation", true, after); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ownershipStoreFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk ownershipStateDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	disk.History[0].Generation = 2
	corrupt, err := json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOwnershipStore(dir, ownershipStoreFixtureBinding()); err == nil {
		t.Fatal("corrupt retained history was accepted")
	}
}

func TestOwnershipStoreZeroAndClosedObjectsFailClosed(t *testing.T) {
	var zero OwnershipStore
	if _, err := zero.Binding(); err == nil {
		t.Fatal("zero store returned a binding")
	}
	_, store := createOwnershipStoreFixture(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scope(); err == nil {
		t.Fatal("closed store returned a scope")
	}
	if _, _, err := store.Checkpoint(); err == nil {
		t.Fatal("closed store returned a checkpoint")
	}
	if _, err := store.Pending(); err == nil {
		t.Fatal("closed store returned pending state")
	}
}
