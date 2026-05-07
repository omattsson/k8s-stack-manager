package sessionstore

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	kindTokenBlock = "token_block"
	kindOIDCState  = "oidc_state"
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
		if err == gorm.ErrRecordNotFound {
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
			_ = s.Cleanup(context.Background())
		}
	}
}
