package mgmtlink

import (
	"database/sql"
	"net/netip"
	"path/filepath"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mgmtlink.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPeerStore_AllocationPersistsAcrossRestart(t *testing.T) {
	db := openDB(t)
	store, err := newPeerStore(db)
	if err != nil {
		t.Fatalf("newPeerStore: %v", err)
	}

	prefix := netip.MustParsePrefix("fd8c:4216:9a10::/48")
	p1, created, err := store.findOrAllocate("uuid:a", "a", "pubkey-a", "1.12.8", prefix, 51820)
	if err != nil || !created {
		t.Fatalf("first allocate: %v created=%v", err, created)
	}

	// A brand-new store over the same database (server restart) must
	// return the same allocation.
	reopened, err := newPeerStore(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	p2, created, err := reopened.findOrAllocate("uuid:a", "a", "pubkey-a-rotated", "1.12.8", prefix, 51820)
	if err != nil {
		t.Fatalf("reallocate: %v", err)
	}
	if created {
		t.Error("existing identity must not be re-created")
	}
	if p1.Prefix != p2.Prefix || p1.StreamAddr != p2.StreamAddr {
		t.Errorf("allocation changed across restart: %s/%s → %s/%s", p1.Prefix, p1.StreamAddr, p2.Prefix, p2.StreamAddr)
	}
	if p2.PubKey != "pubkey-a-rotated" {
		t.Errorf("pubkey rotation not absorbed: %q", p2.PubKey)
	}
}

func TestPeerStore_SequentialDistinctPrefixes(t *testing.T) {
	store, err := newPeerStore(openDB(t))
	if err != nil {
		t.Fatalf("newPeerStore: %v", err)
	}
	prefix := netip.MustParsePrefix("fd8c:4216:9a10::/48")

	seen := map[string]bool{}
	for i, id := range []string{"uuid:1", "uuid:2", "uuid:3"} {
		p, _, err := store.findOrAllocate(id, id[5:], "pk", "", prefix, 51820)
		if err != nil {
			t.Fatalf("allocate %s: %v", id, err)
		}
		if seen[p.Prefix] {
			t.Errorf("duplicate prefix %s", p.Prefix)
		}
		seen[p.Prefix] = true
		want := "fd8c:4216:9a10:" + string(rune('1'+i)) + "::/64"
		if p.Prefix != want {
			t.Errorf("prefix = %q, want %q", p.Prefix, want)
		}
	}
}

func TestIdentityFor(t *testing.T) {
	if got := identityFor("3f0c2f7e-9b1a-4c2d-8e5f-1a2b3c4d5e6f", "tok"); got != "uuid:3f0c2f7e-9b1a-4c2d-8e5f-1a2b3c4d5e6f" {
		t.Errorf("uuid identity = %q", got)
	}
	if got := identityFor("00000000-0000-0000-0000-000000000000", "tok"); got == "" || len(got) != 4+32 {
		t.Errorf("zero uuid must hash the unique token, got %q", got)
	}
	a, b := identityFor("x", "y"), identityFor("x", "y")
	if a != b {
		t.Error("identity must be deterministic")
	}
}

func TestServerKeyPersistence(t *testing.T) {
	db := openDB(t)
	store, err := newPeerStore(db)
	if err != nil {
		t.Fatalf("newPeerStore: %v", err)
	}

	k1, err := loadOrCreateServerKey(store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	k2, err := loadOrCreateServerKey(store)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if k1 != k2 {
		t.Error("server key must survive restart (node-visible identity)")
	}

	fresh, err := newPeerStore(openDB(t))
	if err != nil {
		t.Fatalf("fresh store: %v", err)
	}
	k3, err := loadOrCreateServerKey(fresh)
	if err != nil {
		t.Fatalf("fresh load: %v", err)
	}
	if k3 == k1 {
		t.Error("independent deployments must have independent keys")
	}
	if wgtypes.Key(k3).PublicKey().String() == "" {
		t.Error("key must be usable")
	}
}

func TestConfigValidate(t *testing.T) {
	t.Run("disabled when empty", func(t *testing.T) {
		if (Config{}).Enabled() {
			t.Error("empty config must be disabled")
		}
		if err := (Config{}).Validate(); err != nil {
			t.Errorf("empty config validates clean: %v", err)
		}
	})
	t.Run("partial rejected", func(t *testing.T) {
		err := Config{Prefix: netip.MustParsePrefix("fd8c:4216:9a10::/48")}.Validate()
		if err == nil {
			t.Error("prefix alone must not enable the link")
		}
	})
	t.Run("/64 or longer prefix rejected", func(t *testing.T) {
		err := Config{
			Prefix:    netip.MustParsePrefix("fd8c:4216:9a10:1::/64"),
			JoinToken: "t",
			Listen:    "127.0.0.1:50180",
		}.Validate()
		if err == nil {
			t.Error("prefix must be shorter than /64 to allocate node /64s")
		}
	})
	t.Run("full config accepted", func(t *testing.T) {
		cfg := Config{
			Prefix:    netip.MustParsePrefix("fd8c:4216:9a10::/48"),
			JoinToken: "t",
			Listen:    "127.0.0.1:50180",
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate: %v", err)
		}
	})
}

func TestPrefixNth(t *testing.T) {
	prefix := netip.MustParsePrefix("fd8c:4216:9a10::/48")
	want := map[uint16]string{
		0:     "fd8c:4216:9a10::/64",
		1:     "fd8c:4216:9a10:1::/64",
		65535: "fd8c:4216:9a10:ffff::/64",
	}
	for n, w := range want {
		got, err := prefixNth(prefix, n)
		if err != nil {
			t.Fatalf("prefixNth(%d): %v", n, err)
		}
		if got.String() != w {
			t.Errorf("prefixNth(%d) = %s, want %s", n, got, w)
		}
	}
}
