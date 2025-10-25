package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Server exposes endpoints that mimic the worker's public API surface.
// The worker receives site registration callbacks, triggers sync jobs,
// and mirrors upstream data back into an append-only event store.
type Server struct {
	store         *Store
	builderClient *BuilderClient
}

const maxPageSize = 10

// NewServer creates a worker server with the required collaborators wired in.
func NewServer(store *Store, client *BuilderClient) *Server {
	return &Server{
		store:         store,
		builderClient: client,
	}
}

// Router configures all worker routes.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	r.Route("/worker", func(r chi.Router) {
		r.Get("/sites", s.handleListSites)
		r.Post("/sites", s.handleRegisterSite)
		r.Delete("/sites/{siteID}", s.handleUnregisterSite)

		// Sync endpoints allow external schedulers or cronjobs to tell the worker to ingest
		// data from the builder. All heavy lifting happens inside the handler to keep the flow visible.
		r.Post("/sites/{siteID}/sync/users", s.handleSyncUsers)
		r.Post("/sites/{siteID}/sync/orders", s.handleSyncOrders)

		// Event seeding helpers make it easy to test UTM attribution propagation.
		r.Post("/events/random", s.handleRandomEvent)
		r.Post("/events", s.handleManualEvent)
		r.Get("/events", s.handleListEvents)
	})

	return r
}

func (s *Server) handleRegisterSite(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		SiteID         string `json:"site_id"`
		AccessKey      string `json:"access_key"`
		BuilderBaseURL string `json:"builder_base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: %v", err)
		return
	}

	if strings.TrimSpace(payload.SiteID) == "" ||
		strings.TrimSpace(payload.AccessKey) == "" ||
		strings.TrimSpace(payload.BuilderBaseURL) == "" {
		writeError(w, http.StatusBadRequest, "site_id, access_key, and builder_base_url are required")
		return
	}

	if _, err := url.ParseRequestURI(payload.BuilderBaseURL); err != nil {
		writeError(w, http.StatusBadRequest, "builder_base_url must be a valid URL")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	siteProfile, err := s.builderClient.FetchSiteProfile(ctx, payload.BuilderBaseURL, payload.SiteID, payload.AccessKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "validate against builder: %v", err)
		return
	}

	record := RegisteredSite{
		SiteID:         payload.SiteID,
		AccessKey:      payload.AccessKey,
		BuilderBaseURL: payload.BuilderBaseURL,
		RegisteredAt:   time.Now().UTC(),
	}
	if err := s.store.RegisterSite(r.Context(), record); err != nil {
		writeError(w, http.StatusInternalServerError, "register site: %v", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"site_id":          record.SiteID,
		"builder_base_url": record.BuilderBaseURL,
		"registered_at":    record.RegisteredAt.Format(time.RFC3339),
		"builder_site": map[string]any{
			"id":         siteProfile.ID,
			"name":       siteProfile.Name,
			"created_at": siteProfile.CreatedAt,
		},
	})
}

func (s *Server) handleUnregisterSite(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	if strings.TrimSpace(siteID) == "" {
		writeError(w, http.StatusBadRequest, "site_id required")
		return
	}
	if err := s.store.UnregisterSite(r.Context(), siteID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "site not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "unregister site: %v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list sites: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}

func (s *Server) handleSyncUsers(w http.ResponseWriter, r *http.Request) {
	s.syncEntities(w, r, func(ctx context.Context, site RegisteredSite, page int, start, end *time.Time) (pagedResult, error) {
		resp, err := s.builderClient.FetchUsers(ctx, site.BuilderBaseURL, site.SiteID, site.AccessKey, page, maxPageSize, start, end)
		if err != nil {
			return pagedResult{}, err
		}
		inserted, skipped, err := s.persistUsers(ctx, site, resp.Users)
		if err != nil {
			return pagedResult{}, err
		}
		return pagedResult{
			page:     resp.Page,
			total:    resp.Total,
			hasMore:  resp.HasMore,
			nextPage: resp.NextPage,
			inserted: inserted,
			skipped:  skipped,
		}, nil
	})
}

func (s *Server) handleSyncOrders(w http.ResponseWriter, r *http.Request) {
	s.syncEntities(w, r, func(ctx context.Context, site RegisteredSite, page int, start, end *time.Time) (pagedResult, error) {
		resp, err := s.builderClient.FetchOrders(ctx, site.BuilderBaseURL, site.SiteID, site.AccessKey, page, maxPageSize, start, end)
		if err != nil {
			return pagedResult{}, err
		}
		inserted, skipped, err := s.persistOrders(ctx, site, resp.Orders)
		if err != nil {
			return pagedResult{}, err
		}
		return pagedResult{
			page:     resp.Page,
			total:    resp.Total,
			hasMore:  resp.HasMore,
			nextPage: resp.NextPage,
			inserted: inserted,
			skipped:  skipped,
		}, nil
	})
}

type pagedFetcher func(ctx context.Context, site RegisteredSite, page int, start, end *time.Time) (pagedResult, error)

type pagedResult struct {
	page     int
	total    int
	hasMore  bool
	nextPage *int
	inserted int
	skipped  int
}

// syncEntities is a shared workflow between user and order synchronisation. The comment explains
// the "activity" like flow so non-Go readers can trace the steps.
//
//  1. Determine the target site and interpret optional date filters from the request.
//  2. Loop over pages from the builder API (enforcing the 10 item max) until the remote endpoint
//     signals there are no additional pages.
//  3. Persist each entity as an event while pulling the latest attribution data from the event store.
//  4. Aggregate stats (inserted/skipped counts) and expose them in the HTTP response.
func (s *Server) syncEntities(w http.ResponseWriter, r *http.Request, fetch pagedFetcher) {
	siteID := chi.URLParam(r, "siteID")
	site, err := s.store.GetSite(r.Context(), siteID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "site not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "load site: %v", err)
		return
	}

	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	start, end, err := parseDateRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	summary := SyncSummary{}
	currentPage := page

	ctx := r.Context()
	for {
		res, err := fetch(ctx, site, currentPage, start, end)
		if err != nil {
			writeError(w, http.StatusBadGateway, "fetch remote page: %v", err)
			return
		}
		summary.Inserted += res.inserted
		summary.Skipped += res.skipped
		summary.Pages++
		summary.Total = res.total

		if !res.hasMore {
			break
		}
		if res.nextPage != nil {
			currentPage = *res.nextPage
		} else {
			currentPage++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"site_id": site.SiteID,
		"synced":  summary,
		"filters": map[string]any{
			"start": formatTimePtr(start),
			"end":   formatTimePtr(end),
			"page":  page,
		},
	})
}

func (s *Server) persistUsers(ctx context.Context, site RegisteredSite, users []BuilderUser) (int, int, error) {
	inserted := 0
	skipped := 0
	for _, user := range users {
		utm, ok, err := s.store.LatestAttribution(ctx, user.ID)
		if err != nil {
			return 0, 0, err
		}
		event := Event{
			SiteID:    site.SiteID,
			Timestamp: user.SignupAt,
			UserID:    user.ID,
			EventName: "signup",
			UTMSource: utmIf(ok, utm),
			Properties: map[string]any{
				"email":      user.Email,
				"first_name": user.FirstName,
				"last_name":  user.LastName,
				"signup_at":  user.SignupAt.Format(time.RFC3339),
			},
			DedupeKey: fmt.Sprintf("signup:%s:%s", site.SiteID, user.ID),
		}
		okInserted, err := s.store.InsertEvent(ctx, event)
		if err != nil {
			return 0, 0, err
		}
		if okInserted {
			inserted++
		} else {
			skipped++
		}
	}
	return inserted, skipped, nil
}

func (s *Server) persistOrders(ctx context.Context, site RegisteredSite, orders []BuilderOrder) (int, int, error) {
	inserted := 0
	skipped := 0
	for _, order := range orders {
		utm, ok, err := s.store.LatestAttribution(ctx, order.UserID)
		if err != nil {
			return 0, 0, err
		}
		event := Event{
			SiteID:    site.SiteID,
			Timestamp: order.PlacedAt,
			UserID:    order.UserID,
			EventName: "order_created",
			UTMSource: utmIf(ok, utm),
			Properties: map[string]any{
				"order_id":     order.ID,
				"order_number": order.OrderNumber,
				"total_amount": order.TotalAmount,
				"currency":     order.Currency,
				"user_id":      order.UserID,
				"placed_at":    order.PlacedAt.Format(time.RFC3339),
			},
			DedupeKey: fmt.Sprintf("order:%s:%s", site.SiteID, order.ID),
		}
		okInserted, err := s.store.InsertEvent(ctx, event)
		if err != nil {
			return 0, 0, err
		}
		if okInserted {
			inserted++
		} else {
			skipped++
		}
	}
	return inserted, skipped, nil
}

func utmIf(ok bool, utm string) string {
	if !ok {
		return ""
	}
	return utm
}

func (s *Server) handleRandomEvent(w http.ResponseWriter, r *http.Request) {
	var req RandomEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: %v", err)
		return
	}
	event, err := s.store.InsertRandomAttribution(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, event)
}

func (s *Server) handleManualEvent(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		SiteID     string                 `json:"site_id"`
		Timestamp  string                 `json:"timestamp"`
		UserID     string                 `json:"user_id"`
		EventName  string                 `json:"event_name"`
		UTMSource  string                 `json:"utm_source"`
		Properties map[string]any         `json:"properties"`
		DedupeKey  string                 `json:"dedupe_key"`
		Metadata   map[string]interface{} `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: %v", err)
		return
	}
	if payload.SiteID == "" || payload.UserID == "" || payload.EventName == "" {
		writeError(w, http.StatusBadRequest, "site_id, user_id, and event_name are required")
		return
	}
	ts := time.Now().UTC()
	if payload.Timestamp != "" {
		parsed, err := parseTime(payload.Timestamp)
		if err != nil {
			writeError(w, http.StatusBadRequest, "timestamp: %v", err)
			return
		}
		ts = parsed
	}
	if payload.Properties == nil {
		payload.Properties = map[string]any{}
	}
	dedupe := payload.DedupeKey
	if dedupe == "" {
		dedupe = fmt.Sprintf("manual:%s", uuid.NewString())
	}
	event := Event{
		SiteID:     payload.SiteID,
		Timestamp:  ts,
		UserID:     payload.UserID,
		EventName:  payload.EventName,
		UTMSource:  payload.UTMSource,
		Properties: payload.Properties,
		DedupeKey:  dedupe,
		Metadata:   payload.Metadata,
	}
	inserted, err := s.store.InsertEvent(r.Context(), event)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert event: %v", err)
		return
	}
	status := http.StatusCreated
	if !inserted {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"inserted": inserted,
		"event":    event,
	})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	siteID := r.URL.Query().Get("site_id")
	userID := r.URL.Query().Get("user_id")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	events, err := s.store.ListEvents(r.Context(), siteID, userID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list events: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"count":  len(events),
	})
}

func parseIntDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func parseDateRange(r *http.Request) (*time.Time, *time.Time, error) {
	var startPtr, endPtr *time.Time
	if start := strings.TrimSpace(r.URL.Query().Get("start")); start != "" {
		ts, err := parseTime(start)
		if err != nil {
			return nil, nil, err
		}
		startPtr = &ts
	}
	if end := strings.TrimSpace(r.URL.Query().Get("end")); end != "" {
		ts, err := parseTime(end)
		if err != nil {
			return nil, nil, err
		}
		endPtr = &ts
	}
	return startPtr, endPtr, nil
}

func parseTime(value string) (time.Time, error) {
	formats := []string{time.RFC3339, "2006-01-02"}
	for _, format := range formats {
		if ts, err := time.Parse(format, value); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, errors.New("invalid time format, use RFC3339 or YYYY-MM-DD")
}

func formatTimePtr(ts *time.Time) any {
	if ts == nil {
		return nil
	}
	return ts.Format(time.RFC3339)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": strings.TrimSpace(fmt.Sprintf(format, args...)),
			"status":  status,
		},
	})
}
