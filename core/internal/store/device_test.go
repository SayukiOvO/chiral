package store

import (
	"testing"
	"time"
)

func deviceFixture(t *testing.T) (*Store, User) {
	t.Helper()
	s := testStore(t, storeTestKey)
	u, err := s.CreateUser(User{Name: "mai", Enabled: true}, "sub-token-hash")
	if err != nil {
		t.Fatal(err)
	}
	return s, u
}

func TestRecordDeviceRoundTrips(t *testing.T) {
	s, u := deviceFixture(t)
	now := time.Unix(1_700_000_000, 0)

	if err := s.RecordDevice(u.ID, "203.0.113.7", "tokyo-1", now); err != nil {
		t.Fatal(err)
	}
	devices, err := s.UserDevices(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	if devices[0].IP != "203.0.113.7" {
		t.Errorf("address = %q, want 203.0.113.7", devices[0].IP)
	}
	if devices[0].NodeID != "tokyo-1" {
		t.Errorf("node = %q, want tokyo-1", devices[0].NodeID)
	}
	if devices[0].FirstSeen != now.Unix() || devices[0].LastSeen != now.Unix() {
		t.Errorf("timestamps = %d/%d, want %d", devices[0].FirstSeen, devices[0].LastSeen, now.Unix())
	}
}

// The address is stored under a randomised AEAD, so the primary key has to be
// something stable. If the ciphertext were the key, every poll would insert a
// new row for an address the user has been using all along.
func TestRepeatSightingsDoNotMultiplyRows(t *testing.T) {
	s, u := deviceFixture(t)
	base := time.Unix(1_700_000_000, 0)

	for i := 0; i < 10; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		if err := s.RecordDevice(u.ID, "203.0.113.7", "tokyo-1", at); err != nil {
			t.Fatal(err)
		}
	}
	devices, err := s.UserDevices(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("ten sightings of one address produced %d rows", len(devices))
	}
	if devices[0].FirstSeen != base.Unix() {
		t.Errorf("first_seen = %d, want the original %d", devices[0].FirstSeen, base.Unix())
	}
	want := base.Add(9 * time.Hour).Unix()
	if devices[0].LastSeen != want {
		t.Errorf("last_seen = %d, want the latest %d", devices[0].LastSeen, want)
	}
}

// Without the throttle, a 30-second poll across N nodes writes constantly to a
// handle pinned to one connection.
func TestSightingsInsideTheRefreshWindowDoNotWrite(t *testing.T) {
	s, u := deviceFixture(t)
	base := time.Unix(1_700_000_000, 0)

	if err := s.RecordDevice(u.ID, "203.0.113.7", "tokyo-1", base); err != nil {
		t.Fatal(err)
	}
	// Well inside DeviceRefreshInterval.
	soon := base.Add(time.Minute)
	if err := s.RecordDevice(u.ID, "203.0.113.7", "frankfurt-2", soon); err != nil {
		t.Fatal(err)
	}
	devices, _ := s.UserDevices(u.ID)
	if devices[0].LastSeen != base.Unix() {
		t.Errorf("last_seen = %d; the row was rewritten inside the refresh window", devices[0].LastSeen)
	}

	// Past the window, it updates.
	later := base.Add(DeviceRefreshInterval + time.Second)
	if err := s.RecordDevice(u.ID, "203.0.113.7", "frankfurt-2", later); err != nil {
		t.Fatal(err)
	}
	devices, _ = s.UserDevices(u.ID)
	if devices[0].LastSeen != later.Unix() {
		t.Errorf("last_seen = %d, want %d after the window elapsed", devices[0].LastSeen, later.Unix())
	}
	if devices[0].NodeID != "frankfurt-2" {
		t.Errorf("node = %q, want the refreshed frankfurt-2", devices[0].NodeID)
	}
}

func TestDistinctAddressesAreSeparateRows(t *testing.T) {
	s, u := deviceFixture(t)
	now := time.Unix(1_700_000_000, 0)

	for _, ip := range []string{"203.0.113.7", "198.51.100.4", "2001:db8::1"} {
		if err := s.RecordDevice(u.ID, ip, "tokyo-1", now); err != nil {
			t.Fatal(err)
		}
	}
	devices, _ := s.UserDevices(u.ID)
	if len(devices) != 3 {
		t.Fatalf("got %d rows, want 3", len(devices))
	}
}

// The AAD binds a sealed address to its own row. Moving the ciphertext to
// another user must fail to open rather than decode as that user's address.
func TestASealedAddressCannotBeMovedBetweenUsers(t *testing.T) {
	s := testStore(t, storeTestKey)
	a, err := s.CreateUser(User{Name: "mai", Enabled: true}, "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateUser(User{Name: "lin", Enabled: true}, "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := s.RecordDevice(a.ID, "203.0.113.7", "tokyo-1", now); err != nil {
		t.Fatal(err)
	}

	// Transplant a's ciphertext (and its key) onto b.
	var ipHash, sealed string
	if err := s.db.QueryRow(
		`SELECT ip_hash, ip_enc FROM user_devices WHERE user_id = ?`, a.ID,
	).Scan(&ipHash, &sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO user_devices (user_id, ip_hash, ip_enc, last_node_id, first_seen, last_seen)
		VALUES (?, ?, ?, '', ?, ?)`,
		b.ID, ipHash, sealed, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}

	devices, err := s.UserDevices(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Fatalf("a transplanted address opened for the wrong user: %v", devices)
	}
}

func TestPruneDevicesDropsOnlyTheExpired(t *testing.T) {
	s, u := deviceFixture(t)
	now := time.Unix(1_700_000_000, 0)
	old := now.Add(-DeviceRetention - time.Hour)

	if err := s.RecordDevice(u.ID, "203.0.113.7", "tokyo-1", old); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDevice(u.ID, "198.51.100.4", "tokyo-1", now); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneDevices(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	devices, _ := s.UserDevices(u.ID)
	if len(devices) != 1 || devices[0].IP != "198.51.100.4" {
		t.Fatalf("prune kept the wrong row: %v", devices)
	}
}

// An address is where a specific person lives, not a fleet aggregate. Unlike
// traffic_buckets, which deliberately keeps rows under an empty-string
// sentinel, these must go when their owner does — otherwise they linger for a
// further 30 days, reachable by no UI and therefore reviewed by nobody.
func TestDeletingAUserRemovesTheirAddresses(t *testing.T) {
	s, u := deviceFixture(t)
	now := time.Unix(1_700_000_000, 0)
	if err := s.RecordDevice(u.ID, "203.0.113.7", "tokyo-1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_devices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d address rows survived the user's deletion", n)
	}
}

func TestRecordDeviceRejectsIncompleteInput(t *testing.T) {
	s, u := deviceFixture(t)
	now := time.Unix(1_700_000_000, 0)
	if err := s.RecordDevice("", "203.0.113.7", "tokyo-1", now); err == nil {
		t.Error("accepted a sighting with no user")
	}
	if err := s.RecordDevice(u.ID, "", "tokyo-1", now); err == nil {
		t.Error("accepted a sighting with no address")
	}
}

// device_limit rides along on the ordinary user read/write paths; if it were
// left out of userCols it would silently read back as 0 for everyone.
func TestDeviceLimitPersists(t *testing.T) {
	s := testStore(t, storeTestKey)
	u, err := s.CreateUser(User{Name: "mai", Enabled: true, DeviceLimit: 3}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceLimit != 3 {
		t.Fatalf("device_limit = %d after create, want 3", got.DeviceLimit)
	}

	got.DeviceLimit = 5
	if err := s.UpdateUser(got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUser(u.ID)
	if got.DeviceLimit != 5 {
		t.Fatalf("device_limit = %d after update, want 5", got.DeviceLimit)
	}
}
