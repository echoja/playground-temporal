package builder

import "time"

// Site represents an e-commerce storefront that the builder manages.
type Site struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	AccessKey string    `json:"access_key"`
	CreatedAt time.Time `json:"created_at"`
}

// User models a single customer account stored in the builder DB.
type User struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	SignupAt  time.Time `json:"signup_at"`
}

// Order represents a single checkout event for a customer.
type Order struct {
	ID          string    `json:"id"`
	SiteID      string    `json:"site_id"`
	UserID      string    `json:"user_id"`
	OrderNumber string    `json:"order_number"`
	TotalAmount int64     `json:"total_amount"`
	Currency    string    `json:"currency"`
	PlacedAt    time.Time `json:"placed_at"`
}

// UserPage wraps paginated user results returned to the worker.
type UserPage struct {
	Users     []User `json:"users"`
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
	Total     int    `json:"total"`
	HasMore   bool   `json:"has_more"`
	NextPage  *int   `json:"next_page,omitempty"`
	StartDate string `json:"start_date,omitempty"`
	EndDate   string `json:"end_date,omitempty"`
}

// OrderPage wraps paginated order results returned to the worker.
type OrderPage struct {
	Orders    []Order `json:"orders"`
	Page      int     `json:"page"`
	PageSize  int     `json:"page_size"`
	Total     int     `json:"total"`
	HasMore   bool    `json:"has_more"`
	NextPage  *int    `json:"next_page,omitempty"`
	StartDate string  `json:"start_date,omitempty"`
	EndDate   string  `json:"end_date,omitempty"`
}
