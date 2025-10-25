package builder

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Server exposes HTTP APIs that mimic an external e-commerce site builder.
type Server struct {
	store *Store
}

// NewServer builds a server backed by the provided store.
func NewServer(store *Store) *Server {
	return &Server{store: store}
}

// Router wires all builder routes under a single chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	r.Route("/builder", func(r chi.Router) {
		r.Get("/sites", s.handleListSites)
		r.Post("/sites", s.handleCreateSite)
		r.Route("/sites/{siteID}", func(r chi.Router) {
			r.Get("/", s.handleGetSite)
			r.Delete("/", s.handleDeleteSite)
			r.Post("/random-user", s.handleRandomUser)
			r.Post("/random-order", s.handleRandomOrder)
		})
	})

	r.Route("/builder/api/sites/{siteID}", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAccessKey)
			r.Get("/", s.handleAccessSiteProfile)
			r.Get("/users", s.handleListUsers)
			r.Get("/orders", s.handleListOrders)
		})
	})

	return r
}

func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: %v", err)
		return
	}
	site, err := s.store.CreateSite(r.Context(), payload.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, MarshalSite(site, true))
}

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list sites: %v", err)
		return
	}
	resp := make([]map[string]any, 0, len(sites))
	for _, site := range sites {
		resp = append(resp, MarshalSite(site, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": resp})
}

func (s *Server) handleGetSite(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	site, err := s.store.GetSite(r.Context(), siteID)
	if err != nil {
		handleNotFound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MarshalSite(site, true))
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	err := s.store.DeleteSite(r.Context(), siteID)
	if err != nil {
		handleNotFound(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRandomUser(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	user, err := s.store.CreateRandomUser(r.Context(), siteID)
	if err != nil {
		handleNotFound(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, MarshalUser(user))
}

func (s *Server) handleRandomOrder(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	order, err := s.store.CreateRandomOrder(r.Context(), siteID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, MarshalOrder(order))
}

func (s *Server) handleAccessSiteProfile(w http.ResponseWriter, r *http.Request) {
	site := s.siteFromContext(r.Context())
	writeJSON(w, http.StatusOK, MarshalSite(site, true))
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	site := s.siteFromContext(ctx)
	page, size := parsePaging(r)
	start, end, err := parseDateRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.store.ListUsers(ctx, site.ID, page, size, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list users: %v", err)
		return
	}
	payload := map[string]any{
		"page":      result.Page,
		"page_size": result.PageSize,
		"total":     result.Total,
		"has_more":  result.HasMore,
		"users":     result.Users,
	}
	if result.NextPage != nil {
		payload["next_page"] = result.NextPage
	}
	if result.StartDate != "" {
		payload["start_date"] = result.StartDate
	}
	if result.EndDate != "" {
		payload["end_date"] = result.EndDate
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	site := s.siteFromContext(ctx)
	page, size := parsePaging(r)
	start, end, err := parseDateRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.store.ListOrders(ctx, site.ID, page, size, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list orders: %v", err)
		return
	}
	payload := map[string]any{
		"page":      result.Page,
		"page_size": result.PageSize,
		"total":     result.Total,
		"has_more":  result.HasMore,
		"orders":    result.Orders,
	}
	if result.NextPage != nil {
		payload["next_page"] = result.NextPage
	}
	if result.StartDate != "" {
		payload["start_date"] = result.StartDate
	}
	if result.EndDate != "" {
		payload["end_date"] = result.EndDate
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) requireAccessKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		siteID := chi.URLParam(r, "siteID")
		accessKey := strings.TrimSpace(r.Header.Get("X-Access-Key"))
		if accessKey == "" {
			writeError(w, http.StatusUnauthorized, "missing X-Access-Key header")
			return
		}
		site, err := s.store.ValidateAccessKey(r.Context(), siteID, accessKey)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid site or access key")
			return
		}
		ctx := context.WithValue(r.Context(), siteContextKey{}, site)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) siteFromContext(ctx context.Context) Site {
	return ctx.Value(siteContextKey{}).(Site)
}

type siteContextKey struct{}

func parsePaging(r *http.Request) (int, int) {
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	size := parseIntDefault(r.URL.Query().Get("page_size"), maxPageSize)
	page, size = EnsurePageSize(page, size)
	return page, size
}

func parseIntDefault(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
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

func handleNotFound(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
