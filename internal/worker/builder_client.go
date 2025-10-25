package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BuilderClient captures the HTTP calls the worker issues toward the builder API.
type BuilderClient struct {
	httpClient *http.Client
}

// NewBuilderClient configures a client with sane defaults.
func NewBuilderClient() *BuilderClient {
	return &BuilderClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// BuilderSite describes the metadata returned while validating a site registration.
type BuilderSite struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	AccessKey string    `json:"access_key"`
	CreatedAt time.Time `json:"created_at"`
}

// BuilderUser mirrors the builder's user JSON representation.
type BuilderUser struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	SignupAt  time.Time `json:"signup_at"`
}

// BuilderOrder mirrors builder order JSON.
type BuilderOrder struct {
	ID          string    `json:"id"`
	SiteID      string    `json:"site_id"`
	UserID      string    `json:"user_id"`
	OrderNumber string    `json:"order_number"`
	TotalAmount int64     `json:"total_amount"`
	Currency    string    `json:"currency"`
	PlacedAt    time.Time `json:"placed_at"`
}

// PagedUsersResponse wraps paginated user data.
type PagedUsersResponse struct {
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int           `json:"total"`
	HasMore  bool          `json:"has_more"`
	NextPage *int          `json:"next_page"`
	Users    []BuilderUser `json:"users"`
}

// PagedOrdersResponse wraps paginated orders.
type PagedOrdersResponse struct {
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int            `json:"total"`
	HasMore  bool           `json:"has_more"`
	NextPage *int           `json:"next_page"`
	Orders   []BuilderOrder `json:"orders"`
}

// FetchSiteProfile validates a site ID/access key pairing.
func (c *BuilderClient) FetchSiteProfile(ctx context.Context, baseURL, siteID, accessKey string) (BuilderSite, error) {
	endpoint := fmt.Sprintf("%s/builder/api/sites/%s", strings.TrimRight(baseURL, "/"), url.PathEscape(siteID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return BuilderSite{}, err
	}
	req.Header.Set("X-Access-Key", accessKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return BuilderSite{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return BuilderSite{}, fmt.Errorf("builder responded with %s", resp.Status)
	}
	var site BuilderSite
	if err := json.NewDecoder(resp.Body).Decode(&site); err != nil {
		return BuilderSite{}, fmt.Errorf("decode site profile: %w", err)
	}
	return site, nil
}

// FetchUsers retrieves users with optional date filters.
func (c *BuilderClient) FetchUsers(ctx context.Context, baseURL, siteID, accessKey string, page, pageSize int, start, end *time.Time) (PagedUsersResponse, error) {
	endpoint := fmt.Sprintf("%s/builder/api/sites/%s/users", strings.TrimRight(baseURL, "/"), url.PathEscape(siteID))
	query := make(url.Values)
	query.Set("page", fmt.Sprintf("%d", page))
	query.Set("page_size", fmt.Sprintf("%d", pageSize))
	if start != nil {
		query.Set("start", start.Format(time.RFC3339))
	}
	if end != nil {
		query.Set("end", end.Format(time.RFC3339))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return PagedUsersResponse{}, err
	}
	req.Header.Set("X-Access-Key", accessKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return PagedUsersResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PagedUsersResponse{}, fmt.Errorf("fetch users: builder returned %s", resp.Status)
	}
	var payload PagedUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return PagedUsersResponse{}, fmt.Errorf("decode users: %w", err)
	}
	return payload, nil
}

// FetchOrders retrieves orders with optional date filters.
func (c *BuilderClient) FetchOrders(ctx context.Context, baseURL, siteID, accessKey string, page, pageSize int, start, end *time.Time) (PagedOrdersResponse, error) {
	endpoint := fmt.Sprintf("%s/builder/api/sites/%s/orders", strings.TrimRight(baseURL, "/"), url.PathEscape(siteID))
	query := make(url.Values)
	query.Set("page", fmt.Sprintf("%d", page))
	query.Set("page_size", fmt.Sprintf("%d", pageSize))
	if start != nil {
		query.Set("start", start.Format(time.RFC3339))
	}
	if end != nil {
		query.Set("end", end.Format(time.RFC3339))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return PagedOrdersResponse{}, err
	}
	req.Header.Set("X-Access-Key", accessKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return PagedOrdersResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PagedOrdersResponse{}, fmt.Errorf("fetch orders: builder returned %s", resp.Status)
	}
	var payload PagedOrdersResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return PagedOrdersResponse{}, fmt.Errorf("decode orders: %w", err)
	}
	return payload, nil
}
