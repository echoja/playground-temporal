package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Store encapsulates access to the worker side SQLite database.
type Store struct {
	db *sql.DB
}

// NewStore constructs a worker data access object.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Init applies schema changes for the event and site registry tables.
func (s *Store) Init(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS registered_sites (
			site_id TEXT PRIMARY KEY,
			access_key TEXT NOT NULL,
			builder_base_url TEXT NOT NULL,
			registered_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			site_id TEXT NOT NULL,
			timestamp TIMESTAMP NOT NULL,
			user_id TEXT NOT NULL,
			event_name TEXT NOT NULL,
			utm_source TEXT,
			properties TEXT NOT NULL,
			dedupe_key TEXT NOT NULL UNIQUE,
			ingested_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			metadata TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_events_user ON events(user_id, timestamp DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_events_site ON events(site_id, timestamp DESC);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply worker schema: %w", err)
		}
	}
	return nil
}

// RegisterSite stores builder credentials so the worker can talk to the external API.
func (s *Store) RegisterSite(ctx context.Context, site RegisteredSite) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registered_sites(site_id, access_key, builder_base_url, registered_at) 
		 VALUES(?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP))
		 ON CONFLICT(site_id) DO UPDATE SET access_key = excluded.access_key,
			builder_base_url = excluded.builder_base_url`,
		site.SiteID, site.AccessKey, site.BuilderBaseURL, site.RegisteredAt,
	)
	if err != nil {
		return fmt.Errorf("register site: %w", err)
	}
	return nil
}

// UnregisterSite removes worker credentials and prevents further sync attempts.
func (s *Store) UnregisterSite(ctx context.Context, siteID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM registered_sites WHERE site_id = ?`, siteID)
	if err != nil {
		return fmt.Errorf("unregister site: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetSite fetches a registered site.
func (s *Store) GetSite(ctx context.Context, siteID string) (RegisteredSite, error) {
	var site RegisteredSite
	row := s.db.QueryRowContext(ctx,
		`SELECT site_id, access_key, builder_base_url, registered_at 
		 FROM registered_sites WHERE site_id = ?`, siteID)
	if err := row.Scan(&site.SiteID, &site.AccessKey, &site.BuilderBaseURL, &site.RegisteredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RegisteredSite{}, err
		}
		return RegisteredSite{}, fmt.Errorf("get site: %w", err)
	}
	return site, nil
}

// ListSites returns all registered sites.
func (s *Store) ListSites(ctx context.Context) ([]RegisteredSite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT site_id, access_key, builder_base_url, registered_at 
		 FROM registered_sites ORDER BY registered_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	var sites []RegisteredSite
	for rows.Next() {
		var site RegisteredSite
		if err := rows.Scan(&site.SiteID, &site.AccessKey, &site.BuilderBaseURL, &site.RegisteredAt); err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter sites: %w", err)
	}
	return sites, nil
}

// InsertEvent stores an event unless a duplicate already exists. Returns true when inserted.
func (s *Store) InsertEvent(ctx context.Context, event Event) (bool, error) {
	props, err := json.Marshal(event.Properties)
	if err != nil {
		return false, fmt.Errorf("marshal properties: %w", err)
	}
	var metadata []byte
	if len(event.Metadata) > 0 {
		metadata, err = json.Marshal(event.Metadata)
		if err != nil {
			return false, fmt.Errorf("marshal metadata: %w", err)
		}
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO events(site_id, timestamp, user_id, event_name, utm_source, properties, dedupe_key, ingested_at, metadata)
		 VALUES(?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP), ?)
		 ON CONFLICT(dedupe_key) DO NOTHING`,
		event.SiteID,
		event.Timestamp.UTC(),
		event.UserID,
		event.EventName,
		nullIfEmpty(event.UTMSource),
		string(props),
		event.DedupeKey,
		utcOrNil(event.IngestedAt),
		bytesOrNil(metadata),
	)
	if err != nil {
		return false, fmt.Errorf("insert event: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

func nullIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func utcOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func bytesOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// LatestAttribution returns the most recent non-empty utm_source for a user.
func (s *Store) LatestAttribution(ctx context.Context, userID string) (string, bool, error) {
	var utm sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT utm_source FROM events WHERE user_id = ? AND utm_source IS NOT NULL AND utm_source != '' 
		 ORDER BY timestamp DESC, id DESC LIMIT 1`, userID).Scan(&utm)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("latest attribution: %w", err)
	}
	return utm.String, utm.Valid, nil
}

// InsertRandomAttribution seeds arbitrary browser events used to back-fill utm_source values.
func (s *Store) InsertRandomAttribution(ctx context.Context, req RandomEventRequest) (Event, error) {
	if strings.TrimSpace(req.SiteID) == "" {
		return Event{}, errors.New("site_id required")
	}
	if req.UserID == "" {
		req.UserID = uuid.NewString()
	}
	eventName := req.EventName
	if eventName == "" {
		eventName = randomEventName()
	}
	utm := req.UTMSource
	if utm == "" {
		utm = randomUTM()
	}
	now := time.Now().UTC()
	event := Event{
		SiteID:    req.SiteID,
		Timestamp: now,
		UserID:    req.UserID,
		EventName: eventName,
		UTMSource: utm,
		Properties: map[string]any{
			"session_id": uuid.NewString(),
			"page":       "/landing",
			"referrer":   "https://example.io",
		},
		DedupeKey:  fmt.Sprintf("seed:%s", uuid.NewString()),
		IngestedAt: now,
	}
	inserted, err := s.InsertEvent(ctx, event)
	if err != nil {
		return Event{}, err
	}
	if !inserted {
		return Event{}, errors.New("duplicate random event unexpectedly skipped")
	}
	return event, nil
}

// ListEvents returns events filtered by user or site for debugging.
func (s *Store) ListEvents(ctx context.Context, siteID, userID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	args := []any{}
	clauses := []string{"1 = 1"}
	if siteID != "" {
		clauses = append(clauses, "site_id = ?")
		args = append(args, siteID)
	}
	if userID != "" {
		clauses = append(clauses, "user_id = ?")
		args = append(args, userID)
	}
	query := fmt.Sprintf(`SELECT id, site_id, timestamp, user_id, event_name, utm_source, properties, dedupe_key, ingested_at, metadata 
		FROM events WHERE %s ORDER BY timestamp DESC, id DESC LIMIT ?`, strings.Join(clauses, " AND "))
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var (
			e         Event
			propsJSON string
			metaJSON  sql.NullString
		)
		if err := rows.Scan(
			&e.ID,
			&e.SiteID,
			&e.Timestamp,
			&e.UserID,
			&e.EventName,
			&e.UTMSource,
			&propsJSON,
			&e.DedupeKey,
			&e.IngestedAt,
			&metaJSON,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if err := json.Unmarshal([]byte(propsJSON), &e.Properties); err != nil {
			return nil, fmt.Errorf("decode properties: %w", err)
		}
		if metaJSON.Valid {
			var m map[string]any
			if err := json.Unmarshal([]byte(metaJSON.String), &m); err == nil {
				e.Metadata = m
			}
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter events: %w", err)
	}
	return events, nil
}

var (
	randomEvents = []string{"page_view", "product_view", "basket_add", "checkout_view"}
	randomUTMs   = []string{"google", "facebook", "newsletter", "kakao", "direct"}
)

func randomEventName() string {
	return randomEvents[rand.Intn(len(randomEvents))]
}

func randomUTM() string {
	return randomUTMs[rand.Intn(len(randomUTMs))]
}
