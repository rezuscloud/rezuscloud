// Package tfbackend implements an OpenTofu/Terraform HTTP backend: a remote
// state store that the `tofu` binary writes one encrypted state blob per
// workspace to.
//
// RezusCloud runs this backend so that each tenant's TF state lives inside the
// management plane (ADR 21 — TF state is the single source of truth). The
// backend speaks the protocol the tofu `backend "http"` expects:
//
//   - GET    {addr}?ID={workspace}  -> 200 + state body, or 404 if none
//   - POST   {addr}?ID={workspace}  -> store/overwrite state (request body)
//   - DELETE {addr}?ID={workspace}  -> remove state
//   - LOCK   {addr}?ID={workspace}  -> acquire a lock (LockInfo JSON body);
//     200 if acquired, 423 + existing LockInfo
//     body if already locked
//   - UNLOCK {addr}?ID={workspace}  -> release a lock ({"ID": "..."} body);
//     200 if released, 409 if ID mismatch
//
// The stored blob is opaque encrypted bytes: tofu performs the encryption
// natively (pbkdf2 + aes_gcm) before POSTing. RezusCloud never inspects or
// re-encrypts the payload.
//
// See https://opentofu.org/docs/language/settings/backends/http/ for the
// upstream contract.
package tfbackend

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LockInfo mirrors the lock metadata tofu sends when acquiring a state lock.
// Field names match the upstream JSON keys (capitalised) so tofu's payload
// round-trips without remapping.
type LockInfo struct {
	ID        string    `json:"ID"`
	Operation string    `json:"Operation"`
	Info      string    `json:"Info"`
	Who       string    `json:"Who"`
	Version   string    `json:"Version"`
	Created   time.Time `json:"Created"`
	Path      string    `json:"Path"`
}

// ErrLockHeld is returned by Store.Lock when the workspace is already locked.
// Callers pair it with the existing LockInfo to produce a 423 response.
var ErrLockHeld = errors.New("tfbackend: lock already held")

// Store is the SQLite-backed state and lock store. It owns two tables
// (tf_state, tf_locks) inside the shared management-plane database.
type Store struct {
	db *sql.DB
}

// New constructs a Store over the given database, creating its tables if
// missing. The database is shared with the rest of the management plane
// (state, audit, …); the connection is provided by main.go via store.DB().
func New(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS tf_state (
			id         TEXT NOT NULL PRIMARY KEY,
			state      BLOB NOT NULL,
			updated_at DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS tf_locks (
			id         TEXT NOT NULL PRIMARY KEY,
			lock_id    TEXT NOT NULL,
			info       TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);
	`)
	return err
}

// GetState returns the stored state blob for the workspace.
// found is false (with a nil error) when no state exists yet.
//
// The blob is opaque and self-describing: its serial lives inside the (possibly
// tofu-encrypted) JSON document, not in a separate column — RezusCloud must not
// parse it.
func (s *Store) GetState(ctx context.Context, id string) (state []byte, found bool, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT state FROM tf_state WHERE id = ?`, id)
	err = row.Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return state, true, nil
}

// PutState upserts the opaque state blob for the workspace.
func (s *Store) PutState(ctx context.Context, id string, state []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tf_state (id, state, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			state = excluded.state,
			updated_at = excluded.updated_at
	`, id, state, time.Now().UTC())
	return err
}

// DeleteState removes the state blob. found is false if none existed.
func (s *Store) DeleteState(ctx context.Context, id string) (found bool, err error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tf_state WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Lock attempts to acquire an exclusive lock for the workspace. On success it
// records info and returns (true, nil). If the workspace is already locked it
// returns (false, ErrLockHeld) along with the holder's LockInfo so the caller
// can report the conflict (HTTP 423 + existing info body).
func (s *Store) Lock(ctx context.Context, id string, info LockInfo) (existing *LockInfo, err error) {
	infoJSON, err := marshalLockInfo(info)
	if err != nil {
		return nil, err
	}

	// Insert-or-fail: the PRIMARY KEY on id makes a second lock a conflict.
	_, insertErr := s.db.ExecContext(ctx, `
		INSERT INTO tf_locks (id, lock_id, info, created_at)
		VALUES (?, ?, ?, ?)
	`, id, info.ID, infoJSON, time.Now().UTC())
	if insertErr == nil {
		return nil, nil // acquired
	}

	// Any insert failure here means the row exists (locked). Load the holder.
	holder, loadErr := s.lockHolder(ctx, id)
	if loadErr != nil {
		return nil, loadErr
	}
	return holder, ErrLockHeld
}

// Unlock releases the lock for the workspace, but only if lockID matches the
// held lock's ID. Returns (true, nil) on success; (false, nil) if no lock
// exists; (false, nil) on mismatch after writing nothing. Callers translate
// mismatch into HTTP 409.
func (s *Store) Unlock(ctx context.Context, id, lockID string) (released bool, mismatch bool, err error) {
	holder, err := s.lockHolder(ctx, id)
	if err != nil {
		return false, false, err
	}
	if holder == nil {
		return false, false, nil // nothing to unlock
	}
	if holder.ID != lockID {
		return false, true, nil // mismatch
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM tf_locks WHERE id = ?`, id)
	if err != nil {
		return false, false, err
	}
	return true, false, nil
}

// lockHolder returns the current LockInfo for the workspace, or (nil, nil) if
// the workspace is not locked.
func (s *Store) lockHolder(ctx context.Context, id string) (*LockInfo, error) {
	var infoJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT info FROM tf_locks WHERE id = ?`, id).Scan(&infoJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return unmarshalLockInfo([]byte(infoJSON))
}
