package sessionstore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	kindTokenBlock = "token_block"
	kindOIDCState  = "oidc_state"
	kindUserBlock  = "user_block"
	// kindUserBlockMs holds the block time in Unix milliseconds. The
	// user_block row keeps Unix seconds, so that older versions (rolling
	// update, rollback) keep reading it with their second rule.
	kindUserBlockMs = "user_block_ms"
	kindCLIAuth     = "cli_auth"
)

type SessionEntry struct {
	EntryKey  string `gorm:"column:entry_key;primaryKey;size:255"`
	Kind      string `gorm:"column:kind;primaryKey;size:20"`
	Data      string `gorm:"column:data;type:text"`
	ExpiresAt int64  `gorm:"column:expires_at;not null"`
}

func (SessionEntry) TableName() string { return "session_entries" }

type MySQLStore struct {
	db       *gorm.DB
	done     chan struct{}
	stopOnce sync.Once
}

func NewMySQLStore(db *gorm.DB) *MySQLStore {
	s := &MySQLStore{
		db:   db,
		done: make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

func (s *MySQLStore) BlockToken(ctx context.Context, jti string, expiresAt time.Time) error {
	entry := SessionEntry{
		EntryKey:  jti,
		Kind:      kindTokenBlock,
		ExpiresAt: expiresAt.Unix(),
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "entry_key"}, {Name: "kind"}},
			DoUpdates: clause.AssignmentColumns([]string{"expires_at"}),
		}).
		Create(&entry).Error
}

func (s *MySQLStore) IsTokenBlocked(ctx context.Context, jti string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&SessionEntry{}).
		Where("entry_key = ? AND kind = ? AND expires_at > ?", jti, kindTokenBlock, time.Now().Unix()).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// BlockUser writes two rows with the same key and expiry (dual write; no
// schema change, Data is the text column of session_entries):
//   - kind user_block: the block time in Unix seconds. Older versions read
//     only this row, with their second rule.
//   - kind user_block_ms: the block time in Unix milliseconds. This version
//     reads it for the millisecond rule (see userBlockTime).
func (s *MySQLStore) BlockUser(ctx context.Context, userID string, until time.Time) error {
	now := time.Now()
	entries := []SessionEntry{
		{EntryKey: userID, Kind: kindUserBlock, Data: strconv.FormatInt(now.Unix(), 10), ExpiresAt: until.Unix()},
		{EntryKey: userID, Kind: kindUserBlockMs, Data: strconv.FormatInt(now.UnixMilli(), 10), ExpiresAt: until.Unix()},
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "entry_key"}, {Name: "kind"}},
			DoUpdates: clause.AssignmentColumns([]string{"expires_at", "data"}),
		}).
		Create(&entries).Error
}

// IsUserBlocked returns true when an unexpired user block exists and the
// token was issued at or before the block. See userBlockTime for how the two
// rows combine. A seconds row without a parsable block time (written before
// this field existed) blocks every token.
func (s *MySQLStore) IsUserBlocked(ctx context.Context, userID string, issuedAt time.Time) (bool, error) {
	var entries []SessionEntry
	err := s.db.WithContext(ctx).
		Select("kind, data").
		Where("entry_key = ? AND kind IN ? AND expires_at > ?", userID, []string{kindUserBlock, kindUserBlockMs}, time.Now().Unix()).
		Limit(2).
		Find(&entries).Error
	if err != nil {
		return false, err
	}
	var secData, msData string
	var hasSec, hasMs bool
	for _, e := range entries {
		switch e.Kind {
		case kindUserBlock:
			secData, hasSec = e.Data, true
		case kindUserBlockMs:
			msData, hasMs = e.Data, true
		}
	}
	if !hasSec && !hasMs {
		return false, nil
	}
	return userBlockApplies(userBlockTime(secData, hasSec, msData, hasMs), issuedAt), nil
}

func (s *MySQLStore) UnblockUser(ctx context.Context, userID string) error {
	return s.db.WithContext(ctx).
		Where("entry_key = ? AND kind IN ?", userID, []string{kindUserBlock, kindUserBlockMs}).
		Delete(&SessionEntry{}).Error
}

func (s *MySQLStore) SaveOIDCState(ctx context.Context, state string, data OIDCStateData, ttl time.Duration) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	entry := SessionEntry{
		EntryKey:  state,
		Kind:      kindOIDCState,
		Data:      string(raw),
		ExpiresAt: time.Now().Add(ttl).Unix(),
	}
	return s.db.WithContext(ctx).Create(&entry).Error
}

func (s *MySQLStore) ConsumeOIDCState(ctx context.Context, state string) (*OIDCStateData, error) {
	var entry SessionEntry
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("entry_key = ? AND kind = ? AND expires_at > ?", state, kindOIDCState, time.Now().Unix()).
			First(&entry).Error; err != nil {
			return err
		}
		return tx.Delete(&entry).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var data OIDCStateData
	if err := json.Unmarshal([]byte(entry.Data), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *MySQLStore) SaveCLIAuth(ctx context.Context, sessionID string, data CLIAuthData, ttl time.Duration) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	entry := SessionEntry{
		EntryKey:  sessionID,
		Kind:      kindCLIAuth,
		Data:      string(raw),
		ExpiresAt: time.Now().Add(ttl).Unix(),
	}
	return s.db.WithContext(ctx).Create(&entry).Error
}

func (s *MySQLStore) GetCLIAuth(ctx context.Context, sessionID string) (*CLIAuthData, error) {
	var entry SessionEntry
	err := s.db.WithContext(ctx).
		Where("entry_key = ? AND kind = ? AND expires_at > ?", sessionID, kindCLIAuth, time.Now().Unix()).
		First(&entry).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var data CLIAuthData
	if err := json.Unmarshal([]byte(entry.Data), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *MySQLStore) UpdateCLIAuth(ctx context.Context, sessionID string, data CLIAuthData) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).
		Model(&SessionEntry{}).
		Where("entry_key = ? AND kind = ? AND expires_at > ?", sessionID, kindCLIAuth, time.Now().Unix()).
		Update("data", string(raw))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (s *MySQLStore) ConsumeCLIAuth(ctx context.Context, sessionID string) (*CLIAuthData, error) {
	var entry SessionEntry
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("entry_key = ? AND kind = ? AND expires_at > ?", sessionID, kindCLIAuth, time.Now().Unix()).
			First(&entry).Error; err != nil {
			return err
		}
		return tx.Delete(&entry).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var data CLIAuthData
	if err := json.Unmarshal([]byte(entry.Data), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *MySQLStore) Cleanup(ctx context.Context) error {
	return s.db.WithContext(ctx).
		Where("expires_at < ?", time.Now().Unix()).
		Delete(&SessionEntry{}).Error
}

func (s *MySQLStore) Stop() {
	s.stopOnce.Do(func() { close(s.done) })
}

func (s *MySQLStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			select {
			case <-s.done:
				return
			default:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.Cleanup(ctx); err != nil {
				slog.Warn("Session store cleanup failed", "error", err)
			}
			cancel()
		}
	}
}
