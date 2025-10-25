package builder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	maxPageSize = 10
)

// Store contains all builder-side persistence logic.
type Store struct {
	db  *sql.DB
	rnd *rand.Rand
}

// NewStore wires a builder data store backed by SQLite.
func NewStore(db *sql.DB) *Store {
	return &Store{
		db:  db,
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Init applies schema migrations for the builder database.
func (s *Store) Init(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sites (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			access_key TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			site_id TEXT NOT NULL,
			email TEXT NOT NULL,
			first_name TEXT,
			last_name TEXT,
			signup_at TIMESTAMP NOT NULL,
			FOREIGN KEY(site_id) REFERENCES sites(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_users_site_signup ON users(site_id, signup_at DESC);`,
		`CREATE TABLE IF NOT EXISTS orders (
			id TEXT PRIMARY KEY,
			site_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			order_number TEXT NOT NULL,
			total_amount INTEGER NOT NULL,
			currency TEXT NOT NULL,
			placed_at TIMESTAMP NOT NULL,
			FOREIGN KEY(site_id) REFERENCES sites(id) ON DELETE CASCADE,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_orders_site_placed ON orders(site_id, placed_at DESC);`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply builder schema: %w", err)
		}
	}
	return nil
}

// CreateSite registers a new site and generates its access key.
func (s *Store) CreateSite(ctx context.Context, name string) (Site, error) {
	if strings.TrimSpace(name) == "" {
		return Site{}, errors.New("site name required")
	}
	siteID := uuid.NewString()
	accessKey := uuid.NewString()
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO sites(id, name, access_key, created_at) VALUES (?, ?, ?, ?)`,
		siteID, name, accessKey, now,
	); err != nil {
		return Site{}, fmt.Errorf("insert site: %w", err)
	}
	return Site{
		ID:        siteID,
		Name:      name,
		AccessKey: accessKey,
		CreatedAt: now,
	}, nil
}

// DeleteSite removes a site and cascades related data.
func (s *Store) DeleteSite(ctx context.Context, siteID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE id = ?`, siteID)
	if err != nil {
		return fmt.Errorf("delete site: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListSites returns all registered builder sites.
func (s *Store) ListSites(ctx context.Context) ([]Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, access_key, created_at FROM sites ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	var sites []Site
	for rows.Next() {
		var site Site
		if err := rows.Scan(&site.ID, &site.Name, &site.AccessKey, &site.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter sites: %w", err)
	}
	return sites, nil
}

// GetSite fetches a site by id.
func (s *Store) GetSite(ctx context.Context, siteID string) (Site, error) {
	var site Site
	err := s.db.QueryRowContext(ctx, `SELECT id, name, access_key, created_at FROM sites WHERE id = ?`, siteID).
		Scan(&site.ID, &site.Name, &site.AccessKey, &site.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Site{}, err
		}
		return Site{}, fmt.Errorf("get site: %w", err)
	}
	return site, nil
}

// ValidateAccessKey ensures the provided key belongs to the site.
func (s *Store) ValidateAccessKey(ctx context.Context, siteID, accessKey string) (Site, error) {
	site, err := s.GetSite(ctx, siteID)
	if err != nil {
		return Site{}, err
	}
	if site.AccessKey != accessKey {
		return Site{}, errors.New("invalid access key")
	}
	return site, nil
}

// EnsurePageSize enforces the maximum page size contract.
func EnsurePageSize(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = maxPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// ListUsers returns paginated user rows filtered by date constraints.
func (s *Store) ListUsers(ctx context.Context, siteID string, page, pageSize int, start, end *time.Time) (UserPage, error) {
	page, pageSize = EnsurePageSize(page, pageSize)
	args := []any{siteID}
	clauses := []string{"site_id = ?"}
	if start != nil {
		clauses = append(clauses, "signup_at >= ?")
		args = append(args, start.UTC())
	}
	if end != nil {
		clauses = append(clauses, "signup_at <= ?")
		args = append(args, end.UTC())
	}

	where := strings.Join(clauses, " AND ")

	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM users WHERE %s`, where)
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return UserPage{}, fmt.Errorf("count users: %w", err)
	}

	offset := (page - 1) * pageSize
	dataQuery := fmt.Sprintf(`SELECT id, site_id, email, first_name, last_name, signup_at 
		FROM users WHERE %s ORDER BY signup_at DESC, id LIMIT ? OFFSET ?`, where)
	argsWithPaging := append(append([]any{}, args...), pageSize, offset)
	rows, err := s.db.QueryContext(ctx, dataQuery, argsWithPaging...)
	if err != nil {
		return UserPage{}, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0, pageSize)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.SiteID, &u.Email, &u.FirstName, &u.LastName, &u.SignupAt); err != nil {
			return UserPage{}, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return UserPage{}, fmt.Errorf("iter users: %w", err)
	}

	hasMore := offset+len(users) < total
	var nextPage *int
	if hasMore {
		n := page + 1
		nextPage = &n
	}

	pageResp := UserPage{
		Users:    users,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
		HasMore:  hasMore,
	}
	if hasMore {
		pageResp.NextPage = nextPage
	}
	if start != nil {
		pageResp.StartDate = start.Format(time.RFC3339)
	}
	if end != nil {
		pageResp.EndDate = end.Format(time.RFC3339)
	}
	return pageResp, nil
}

// ListOrders returns paginated orders filtered by placed_at range.
func (s *Store) ListOrders(ctx context.Context, siteID string, page, pageSize int, start, end *time.Time) (OrderPage, error) {
	page, pageSize = EnsurePageSize(page, pageSize)
	args := []any{siteID}
	clauses := []string{"site_id = ?"}
	if start != nil {
		clauses = append(clauses, "placed_at >= ?")
		args = append(args, start.UTC())
	}
	if end != nil {
		clauses = append(clauses, "placed_at <= ?")
		args = append(args, end.UTC())
	}
	where := strings.Join(clauses, " AND ")

	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM orders WHERE %s`, where)
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return OrderPage{}, fmt.Errorf("count orders: %w", err)
	}

	offset := (page - 1) * pageSize
	dataQuery := fmt.Sprintf(`SELECT id, site_id, user_id, order_number, total_amount, currency, placed_at 
		FROM orders WHERE %s ORDER BY placed_at DESC, id LIMIT ? OFFSET ?`, where)
	argsWithPaging := append(append([]any{}, args...), pageSize, offset)
	rows, err := s.db.QueryContext(ctx, dataQuery, argsWithPaging...)
	if err != nil {
		return OrderPage{}, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	orders := make([]Order, 0, pageSize)
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.SiteID, &o.UserID, &o.OrderNumber, &o.TotalAmount, &o.Currency, &o.PlacedAt); err != nil {
			return OrderPage{}, fmt.Errorf("scan order: %w", err)
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return OrderPage{}, fmt.Errorf("iter orders: %w", err)
	}

	hasMore := offset+len(orders) < total
	var nextPage *int
	if hasMore {
		n := page + 1
		nextPage = &n
	}

	resp := OrderPage{
		Orders:   orders,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
		HasMore:  hasMore,
	}
	if hasMore {
		resp.NextPage = nextPage
	}
	if start != nil {
		resp.StartDate = start.Format(time.RFC3339)
	}
	if end != nil {
		resp.EndDate = end.Format(time.RFC3339)
	}
	return resp, nil
}

var (
	firstNames = []string{"Alex", "Jordan", "Taylor", "Morgan", "Jamie", "Avery", "Casey", "Dylan", "Riley", "Skyler"}
	lastNames  = []string{"Kim", "Lee", "Park", "Choi", "Smith", "Garcia", "Williams", "Chen", "Nguyen", "Johnson"}
	domains    = []string{"example.com", "shoptest.co", "playground.dev"}
	currencies = []string{"USD", "KRW", "JPY"}
)

// CreateRandomUser seeds a random user for a site.
func (s *Store) CreateRandomUser(ctx context.Context, siteID string) (User, error) {
	if _, err := s.GetSite(ctx, siteID); err != nil {
		return User{}, err
	}
	userID := uuid.NewString()
	first := firstNames[s.rnd.Intn(len(firstNames))]
	last := lastNames[s.rnd.Intn(len(lastNames))]
	emailLocal := fmt.Sprintf("%s.%s+%04d", strings.ToLower(first), strings.ToLower(last), s.rnd.Intn(10000))
	email := fmt.Sprintf("%s@%s", emailLocal, domains[s.rnd.Intn(len(domains))])
	signupAt := randomTimeInPast(s.rnd, 120*24*time.Hour)

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO users(id, site_id, email, first_name, last_name, signup_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, siteID, strings.ToLower(email), first, last, signupAt,
	); err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}

	return User{
		ID:        userID,
		SiteID:    siteID,
		Email:     strings.ToLower(email),
		FirstName: first,
		LastName:  last,
		SignupAt:  signupAt,
	}, nil
}

// CreateRandomOrder creates a random order for an existing user in the site.
func (s *Store) CreateRandomOrder(ctx context.Context, siteID string) (Order, error) {
	if _, err := s.GetSite(ctx, siteID); err != nil {
		return Order{}, err
	}
	user, err := s.pickRandomUser(ctx, siteID)
	if err != nil {
		return Order{}, fmt.Errorf("pick user: %w", err)
	}
	orderID := uuid.NewString()
	orderNumber := fmt.Sprintf("ORD-%s", strings.ToUpper(uuid.NewString())[:8])
	total := int64(1000 + s.rnd.Intn(150000))
	currency := currencies[s.rnd.Intn(len(currencies))]
	placedAt := randomTimeNear(s.rnd, time.Now().UTC(), 45*24*time.Hour)

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO orders(id, site_id, user_id, order_number, total_amount, currency, placed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		orderID, siteID, user.ID, orderNumber, total, currency, placedAt,
	); err != nil {
		return Order{}, fmt.Errorf("insert order: %w", err)
	}

	return Order{
		ID:          orderID,
		SiteID:      siteID,
		UserID:      user.ID,
		OrderNumber: orderNumber,
		TotalAmount: total,
		Currency:    currency,
		PlacedAt:    placedAt,
	}, nil
}

func (s *Store) pickRandomUser(ctx context.Context, siteID string) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, site_id, email, first_name, last_name, signup_at FROM users WHERE site_id = ? ORDER BY RANDOM() LIMIT 1`, siteID)
	var u User
	if err := row.Scan(&u.ID, &u.SiteID, &u.Email, &u.FirstName, &u.LastName, &u.SignupAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, errors.New("no users available for site")
		}
		return User{}, err
	}
	return u, nil
}

func randomTimeInPast(r *rand.Rand, maxSpan time.Duration) time.Time {
	now := time.Now().UTC()
	diff := time.Duration(r.Int63n(int64(maxSpan)))
	return now.Add(-diff)
}

func randomTimeNear(r *rand.Rand, ref time.Time, span time.Duration) time.Time {
	offset := time.Duration(r.Int63n(int64(span)))
	return ref.Add(-offset)
}

// MarshalSite provides a JSON-friendly representation hiding the access key by default.
func MarshalSite(site Site, includeKey bool) map[string]any {
	payload := map[string]any{
		"id":         site.ID,
		"name":       site.Name,
		"created_at": site.CreatedAt.Format(time.RFC3339),
	}
	if includeKey {
		payload["access_key"] = site.AccessKey
	}
	return payload
}

// MarshalUser converts a user into a map for consistent JSON responses.
func MarshalUser(u User) map[string]any {
	return map[string]any{
		"id":         u.ID,
		"site_id":    u.SiteID,
		"email":      u.Email,
		"first_name": u.FirstName,
		"last_name":  u.LastName,
		"signup_at":  u.SignupAt.Format(time.RFC3339),
	}
}

// MarshalOrder converts an order to a JSON map.
func MarshalOrder(o Order) map[string]any {
	return map[string]any{
		"id":           o.ID,
		"site_id":      o.SiteID,
		"user_id":      o.UserID,
		"order_number": o.OrderNumber,
		"total_amount": o.TotalAmount,
		"currency":     o.Currency,
		"placed_at":    o.PlacedAt.Format(time.RFC3339),
	}
}
