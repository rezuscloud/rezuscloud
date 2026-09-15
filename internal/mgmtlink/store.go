package mgmtlink

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// peerStore persists the node→address allocation in the platform's SQLite DB
// (own tables, same database — the audit precedent). Allocations are keyed by
// a stable node identity and survive restarts: a node's /64 never changes
// once assigned.
type peerStore struct {
	db *sql.DB
}

type peer struct {
	Identity   string // node_uuid, or sha256(unique_token) when the UUID is absent/zeroed
	NodeUUID   string
	PubKey     string
	Prefix     string // node's /64, e.g. fd8c:4216:9a10:1::/64
	StreamAddr string // virtual addr:port for tunnel-mode nodes
	JoinedAt   time.Time
	LastSeen   time.Time
}

const peerSchema = `
CREATE TABLE IF NOT EXISTS mgmtlink_peers (
	identity   TEXT PRIMARY KEY,
	node_uuid  TEXT NOT NULL DEFAULT '',
	pubkey     TEXT NOT NULL,
	prefix     TEXT NOT NULL,
	node_addr  TEXT NOT NULL,
	idx        INTEGER NOT NULL UNIQUE,
	joined_at  TEXT NOT NULL,
	last_seen  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mgmtlink_settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

func newPeerStore(db *sql.DB) (*peerStore, error) {
	if _, err := db.Exec(peerSchema); err != nil {
		return nil, fmt.Errorf("mgmtlink schema: %w", err)
	}
	return &peerStore{db: db}, nil
}

// identityFor derives the stable node identity. Talos sends a machine UUID,
// but it can be all-zeroes on machines without one — the unique machine token
// (a per-install random value) is the fallback, exactly as upstream.
func identityFor(nodeUUID, uniqueToken string) string {
	if nodeUUID != "" && !isZeroUUID(nodeUUID) {
		return "uuid:" + strings.ToLower(nodeUUID)
	}
	sum := sha256.Sum256([]byte("tok:" + uniqueToken))
	return "tok:" + hex.EncodeToString(sum[:16])
}

func isZeroUUID(s string) bool {
	s = strings.ReplaceAll(strings.ToLower(s), "-", "0")
	for _, c := range s {
		if c != '0' {
			return false
		}
	}
	return true
}

// findOrAllocate returns the peer for the identity, allocating the next /64
// and stream address on first contact. pubkey updates are absorbed (key
// rotation re-provisions the same identity).
func (s *peerStore) findOrAllocate(identity, nodeUUID, pubkey, talosVersion string, prefix netip.Prefix, streamPort uint16) (*peer, bool, error) {
	now := time.Now().UTC()

	var p peer
	var joined, lastSeen string
	err := s.db.QueryRow(
		`SELECT identity, node_uuid, pubkey, prefix, node_addr, joined_at, last_seen FROM mgmtlink_peers WHERE identity = ?`,
		identity,
	).Scan(&p.Identity, &p.NodeUUID, &p.PubKey, &p.Prefix, &p.StreamAddr, &joined, &lastSeen)
	p.JoinedAt = parseStamp(joined)
	p.LastSeen = parseStamp(lastSeen)

	switch {
	case err == nil:
		if p.PubKey != pubkey {
			if _, err := s.db.Exec(`UPDATE mgmtlink_peers SET pubkey = ?, last_seen = ? WHERE identity = ?`,
				pubkey, now.Format(time.RFC3339), identity); err != nil {
				return nil, false, fmt.Errorf("update peer pubkey: %w", err)
			}
			p.PubKey = pubkey
		}
		return &p, false, nil

	case isNoRows(err):
		idx, err := s.nextIndex()
		if err != nil {
			return nil, false, err
		}
		nodePrefix, err := prefixNth(prefix, idx)
		if err != nil {
			return nil, false, err
		}
		streamAddr := netip.AddrPortFrom(nodePrefix.Addr(), streamPort)

		p = peer{
			Identity:   identity,
			NodeUUID:   nodeUUID,
			PubKey:     pubkey,
			Prefix:     nodePrefix.String(),
			StreamAddr: streamAddr.String(),
		}
		_, err = s.db.Exec(
			`INSERT INTO mgmtlink_peers (identity, node_uuid, pubkey, prefix, node_addr, idx, joined_at, last_seen) VALUES (?,?,?,?,?,?,?,?)`,
			p.Identity, p.NodeUUID, p.PubKey, p.Prefix, p.StreamAddr, idx,
			now.Format(time.RFC3339), now.Format(time.RFC3339),
		)
		if err != nil {
			return nil, false, fmt.Errorf("insert peer: %w", err)
		}
		return &p, true, nil

	default:
		return nil, false, fmt.Errorf("query peer: %w", err)
	}
}

func isNoRows(err error) bool { return err == sql.ErrNoRows }

// parseStamp parses stored RFC3339 timestamps; zero on any mismatch.
func parseStamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *peerStore) nextIndex() (uint16, error) {
	var maxIdx sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(idx) FROM mgmtlink_peers`).Scan(&maxIdx); err != nil {
		return 0, err
	}
	if !maxIdx.Valid {
		return 1, nil // index 0 is the server's own /64
	}
	if maxIdx.Int64 >= 65535 {
		return 0, fmt.Errorf("mgmtlink prefix exhausted (%d /64s allocated)", maxIdx.Int64)
	}
	return uint16(maxIdx.Int64 + 1), nil
}

// prefixNth returns the n-th /64 inside a prefix shorter than /64.
func prefixNth(prefix netip.Prefix, n uint16) (netip.Prefix, error) {
	if prefix.Bits() >= 64 {
		return netip.Prefix{}, fmt.Errorf("mgmtlink prefix must be shorter than /64, got %s", prefix)
	}
	base := prefix.Masked().Addr().As16()
	base[6] = byte(n >> 8)
	base[7] = byte(n)
	addr, ok := netip.AddrFromSlice(base[:])
	if !ok {
		return netip.Prefix{}, fmt.Errorf("mgmtlink prefix computation failed for %s", prefix)
	}
	return netip.PrefixFrom(addr.Unmap(), 64), nil
}

func (s *peerStore) touch(identity string) {
	_, _ = s.db.Exec(`UPDATE mgmtlink_peers SET last_seen = ? WHERE identity = ?`,
		time.Now().UTC().Format(time.RFC3339), identity)
}

func (s *peerStore) list() ([]*peer, error) {
	rows, err := s.db.Query(`SELECT identity, node_uuid, pubkey, prefix, node_addr, joined_at, last_seen FROM mgmtlink_peers ORDER BY joined_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*peer
	for rows.Next() {
		var p peer
		var joined, lastSeen string
		if err := rows.Scan(&p.Identity, &p.NodeUUID, &p.PubKey, &p.Prefix, &p.StreamAddr, &joined, &lastSeen); err != nil {
			return nil, err
		}
		p.JoinedAt = parseStamp(joined)
		p.LastSeen = parseStamp(lastSeen)
		out = append(out, &p)
	}
	return out, rows.Err()
}

// settings persists single values (currently the server's WG private key so
// node-visible identity survives restarts).
func (s *peerStore) setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM mgmtlink_settings WHERE key = ?`, key).Scan(&v)
	if isNoRows(err) {
		return "", nil
	}
	return v, err
}

func (s *peerStore) setSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO mgmtlink_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}
