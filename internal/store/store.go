// Package store owns each user's encrypted SQLite database.
//
// Layout on disk (under the configured data directory):
//
//	users/<hash(userID)>/finance.db   AES-256-XTS encrypted SQLite database
//	users/<hash(userID)>/dek.json     that database's key, wrapped by the master key
//
// Directory names are a hash of the SSO user ID, so neither usernames nor
// anything about the data are visible on the volume. Every byte of the
// database (and its WAL/journal) is encrypted page-by-page by the
// ncruces/go-sqlite3 "xts" VFS; see internal/keystore for key handling.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/xts" // registers the encrypting "xts" VFS

	"github.com/nathanamorin/finance-tracker/internal/keystore"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrWrongKey means the database exists but could not be decrypted with
// the user's key (e.g. dek.json was replaced or the file is corrupt).
var ErrWrongKey = errors.New("store: database could not be decrypted with this user's key")

// Store is one user's open database.
type Store struct {
	db  *sql.DB
	loc *time.Location
}

// DB exposes the underlying handle (for tests and ad-hoc tooling).
func (s *Store) DB() *sql.DB { return s.db }

// Manager opens per-user stores on demand and closes idle ones, so
// decrypted keys and connections only stay in memory while a user is
// active.
type Manager struct {
	dataDir string
	keys    *keystore.KeyStore
	idle    time.Duration
	loc     *time.Location

	mu   sync.Mutex
	open map[string]*handle
}

type handle struct {
	store    *Store
	refs     int
	lastUsed time.Time
}

// NewManager creates a Manager. idle is how long an unused database stays
// open; loc is the time zone used for "today" in reports.
func NewManager(dataDir string, keys *keystore.KeyStore, idle time.Duration, loc *time.Location) (*Manager, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "users"), 0o700); err != nil {
		return nil, err
	}
	if loc == nil {
		loc = time.Local
	}
	return &Manager{dataDir: dataDir, keys: keys, idle: idle, loc: loc, open: map[string]*handle{}}, nil
}

// UserDir returns the directory holding userID's database and key.
func (m *Manager) UserDir(userID string) string {
	sum := sha256.Sum256([]byte("fin-tracker/user/" + userID))
	return filepath.Join(m.dataDir, "users", hex.EncodeToString(sum[:16]))
}

// Acquire returns userID's store, opening (and creating/migrating) it if
// needed. The caller must call release when done.
func (m *Manager) Acquire(ctx context.Context, userID string) (s *Store, release func(), err error) {
	if userID == "" {
		return nil, nil, errors.New("store: empty user ID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.open[userID]
	if !ok {
		st, err := m.openUser(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		h = &handle{store: st}
		m.open[userID] = h
	}
	h.refs++
	h.lastUsed = time.Now()

	var once sync.Once
	return h.store, func() {
		once.Do(func() {
			m.mu.Lock()
			h.refs--
			h.lastUsed = time.Now()
			m.mu.Unlock()
		})
	}, nil
}

func (m *Manager) openUser(ctx context.Context, userID string) (*Store, error) {
	dir := m.UserDir(userID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dek, created, err := m.keys.LoadOrCreate(filepath.Join(dir, "dek.json"), userID)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "finance.db")
	if created {
		if _, err := os.Stat(dbPath); err == nil {
			// A database without its key is unrecoverable; refuse rather
			// than silently starting over on top of it.
			return nil, fmt.Errorf("store: %s exists but its key file was missing; refusing to create a new key", dbPath)
		}
		slog.Info("created encryption key for new user database", "dir", filepath.Base(dir))
	}
	st, err := Open(ctx, dbPath, dek, m.loc)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Open opens (creating if needed) an encrypted database at path using the
// raw 64-byte AES-XTS key, and applies pending migrations.
func Open(ctx context.Context, path string, key []byte, loc *time.Location) (*Store, error) {
	if len(key) != keystore.DEKSize {
		return nil, fmt.Errorf("store: key must be %d bytes", keystore.DEKSize)
	}
	hexKey := hex.EncodeToString(key)

	// The key is set with a PRAGMA on each new connection rather than in
	// the URI, so it isn't visible through the filename/URI parameters.
	dsn := "file:" + filepath.ToSlash(path) + "?vfs=xts&_txlock=immediate"
	db, err := driver.Open(dsn, func(c *sqlite3.Conn) error {
		return c.Exec(`PRAGMA hexkey='` + hexKey + `';` +
			`PRAGMA busy_timeout=10000;` +
			`PRAGMA foreign_keys=ON;` +
			`PRAGMA temp_store=memory;` + // keep temp tables off disk
			`PRAGMA journal_mode=WAL;` +
			`PRAGMA synchronous=NORMAL;`)
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	// The first real read fails with SQLITE_NOTADB if the key is wrong.
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		db.Close()
		if errors.Is(err, sqlite3.NOTADB) {
			return nil, ErrWrongKey
		}
		return nil, err
	}

	if loc == nil {
		loc = time.Local
	}
	s := &Store{db: db, loc: loc}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrating: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies migrations/NNNN_*.sql in order, tracked by PRAGMA user_version.
func (s *Store) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	type mig struct {
		version int
		name    string
	}
	var migs []mig
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return fmt.Errorf("bad migration filename %q", e.Name())
		}
		migs = append(migs, mig{v, e.Name()})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	var current int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return err
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Run closes databases that have been idle longer than the idle timeout,
// until ctx is cancelled. Callers close remaining databases with CloseAll
// once in-flight requests have drained.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.reap()
		}
	}
}

func (m *Manager) reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, h := range m.open {
		if h.refs == 0 && time.Since(h.lastUsed) > m.idle {
			if err := h.store.Close(); err != nil {
				slog.Warn("closing idle database", "err", err)
			}
			delete(m.open, id)
		}
	}
}

// CloseAll closes every open database.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, h := range m.open {
		h.store.Close()
		delete(m.open, id)
	}
}

// OpenCount reports how many user databases are currently open.
func (m *Manager) OpenCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.open)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
