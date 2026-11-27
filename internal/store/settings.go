package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// MaxSettingBytes bounds a single setting's JSON size.
const MaxSettingBytes = 64 << 10

var settingKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// GetSetting returns the stored JSON for key, or (nil, nil) if unset.
func (s *Store) GetSetting(ctx context.Context, key string) (json.RawMessage, error) {
	if !settingKeyRE.MatchString(key) {
		return nil, fmt.Errorf("%w: bad setting key", ErrInvalid)
	}
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(v), nil
}

// PutSetting stores a JSON value for key (must be valid JSON, ≤ 64 KB).
func (s *Store) PutSetting(ctx context.Context, key string, value json.RawMessage) error {
	if !settingKeyRE.MatchString(key) {
		return fmt.Errorf("%w: bad setting key", ErrInvalid)
	}
	if len(value) > MaxSettingBytes {
		return fmt.Errorf("%w: setting too large", ErrInvalid)
	}
	if !json.Valid(value) {
		return fmt.Errorf("%w: setting must be valid JSON", ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		key, string(value), nowRFC3339())
	return err
}
