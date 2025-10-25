package worker

import "time"

// RegisteredSite stores credentials that let the worker talk to the builder API.
type RegisteredSite struct {
	SiteID         string    `json:"site_id"`
	AccessKey      string    `json:"access_key"`
	BuilderBaseURL string    `json:"builder_base_url"`
	RegisteredAt   time.Time `json:"registered_at"`
}

// Event models a single append-only row in the event database.
type Event struct {
	ID         int64                  `json:"id,omitempty"`
	SiteID     string                 `json:"site_id"`
	Timestamp  time.Time              `json:"timestamp"`
	UserID     string                 `json:"user_id"`
	EventName  string                 `json:"event_name"`
	UTMSource  string                 `json:"utm_source,omitempty"`
	Properties map[string]any         `json:"properties"`
	DedupeKey  string                 `json:"dedupe_key"`
	IngestedAt time.Time              `json:"ingested_at"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// SyncSummary aggregates the effects of a sync pass.
type SyncSummary struct {
	Inserted int `json:"inserted"`
	Skipped  int `json:"skipped"`
	Pages    int `json:"pages_processed"`
	Total    int `json:"total_remote"`
}

// RandomEventRequest describes the payload used to seed ad-hoc events.
type RandomEventRequest struct {
	SiteID    string `json:"site_id"`
	UserID    string `json:"user_id,omitempty"`
	EventName string `json:"event_name,omitempty"`
	UTMSource string `json:"utm_source,omitempty"`
}
