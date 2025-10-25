# API Reference

This document describes every test-only HTTP endpoint exposed by the builder and worker services. All examples assume the builder is listening on `http://localhost:8081` and the worker on `http://localhost:8080`.

## Common Conventions
- All endpoints speak JSON and expect the `Content-Type: application/json` header on requests with bodies.
- Timestamps use RFC3339 (e.g., `2025-10-25T09:00:00Z`).
- Pagination always caps `page_size` at **10** items.

---

## Builder Service

### Health Check
- **GET** `/healthz`
- Returns `{ "ok": true }` when the service is ready.

### Admin Endpoints (no auth)

#### Create Site
- **POST** `/builder/sites`
- **Body**
  ```json
  {
    "name": "My Demo Store"
  }
  ```
- **201 Response**
  ```json
  {
    "id": "2f3...",
    "name": "My Demo Store",
    "access_key": "5e8...",
    "created_at": "2025-10-25T09:00:00Z"
  }
  ```

#### List Sites
- **GET** `/builder/sites`
- **200 Response**
  ```json
  {
    "sites": [ {
      "id": "2f3...",
      "name": "My Demo Store",
      "access_key": "5e8...",
      "created_at": "2025-10-25T09:00:00Z"
    } ]
  }
  ```

#### Get Site
- **GET** `/builder/sites/{siteID}`
- Returns the same payload as creation.

#### Delete Site
- **DELETE** `/builder/sites/{siteID}`
- **204 No Content** on success.

#### Seed Random User
- **POST** `/builder/sites/{siteID}/random-user`
- **201 Response**
  ```json
  {
    "id": "usr...",
    "site_id": "2f3...",
    "email": "alex.kim+0421@example.com",
    "first_name": "Alex",
    "last_name": "Kim",
    "signup_at": "2025-09-12T18:22:11Z"
  }
  ```

#### Seed Random Order
- **POST** `/builder/sites/{siteID}/random-order`
- **201 Response**
  ```json
  {
    "id": "ord...",
    "site_id": "2f3...",
    "user_id": "usr...",
    "order_number": "ORD-AB12CD34",
    "total_amount": 42800,
    "currency": "USD",
    "placed_at": "2025-10-20T04:11:19Z"
  }
  ```

### Worker-Facing Builder API (requires `X-Access-Key` header)

#### Get Site Profile
- **GET** `/builder/api/sites/{siteID}`
- **Headers**: `X-Access-Key: <site.access_key>`
- **200 Response**
  ```json
  {
    "id": "2f3...",
    "name": "My Demo Store",
    "access_key": "5e8...",
    "created_at": "2025-10-25T09:00:00Z"
  }
  ```

#### List Users
- **GET** `/builder/api/sites/{siteID}/users`
- **Headers**: `X-Access-Key`
- **Query**: `page` (default 1), `page_size` (default 10, max 10), optional `start`, `end` (timestamp filters)
- **200 Response**
  ```json
  {
    "page": 1,
    "page_size": 10,
    "total": 27,
    "has_more": true,
    "next_page": 2,
    "users": [ { ... up to 10 users ... } ]
  }
  ```

#### List Orders
- **GET** `/builder/api/sites/{siteID}/orders`
- Same parameters/shape as `/users`, but returns `orders`.

---

## Worker Service

### Health Check
- **GET** `/healthz`
- Returns `{ "ok": true }`.

### Site Registry

#### Register Site
- **POST** `/worker/sites`
- **Body**
  ```json
  {
    "site_id": "2f3...",
    "access_key": "5e8...",
    "builder_base_url": "http://localhost:8081"
  }
  ```
- Validates credentials against the builder. Fails with **502** if the builder rejects the access key.
- **201 Response**
  ```json
  {
    "site_id": "2f3...",
    "builder_base_url": "http://localhost:8081",
    "registered_at": "2025-10-25T09:05:00Z",
    "builder_site": {
      "id": "2f3...",
      "name": "My Demo Store",
      "created_at": "2025-10-25T09:00:00Z"
    }
  }
  ```

#### List Registered Sites
- **GET** `/worker/sites`
- **200 Response**: `{ "sites": [ {"site_id": ..., "access_key": ..., "builder_base_url": ..., "registered_at": ...} ] }`

#### Unregister Site
- **DELETE** `/worker/sites/{siteID}`
- **204 No Content**, or **404** if the site is unknown.

### Sync APIs

> These endpoints contact the builder and insert deduplicated events into `events.db`. They accept optional filters:
> - `page`: starting page (defaults to 1)
> - `start`, `end`: filter window applied to both users and orders depending on the endpoint.

#### Sync Users
- **POST** `/worker/sites/{siteID}/sync/users`
- **200 Response**
  ```json
  {
    "site_id": "2f3...",
    "synced": {
      "inserted": 10,
      "skipped": 0,
      "pages_processed": 3,
      "total_remote": 27
    },
    "filters": {
      "start": null,
      "end": null,
      "page": 1
    }
  }
  ```

#### Sync Orders
- **POST** `/worker/sites/{siteID}/sync/orders`
- Response identical in shape to the user sync.

### Event Utilities

#### Seed Random Attribution Event
- **POST** `/worker/events/random`
- **Body** *(all fields optional except `site_id`)*
  ```json
  {
    "site_id": "2f3...",
    "user_id": "usr...",
    "event_name": "page_view",
    "utm_source": "google"
  }
  ```
- **201 Response**: Fully populated event including generated `dedupe_key` and `properties`.

#### Insert Manual Event
- **POST** `/worker/events`
- **Body**
  ```json
  {
    "site_id": "2f3...",
    "timestamp": "2025-10-25T09:10:00Z",
    "user_id": "usr...",
    "event_name": "custom_event",
    "utm_source": "newsletter",
    "properties": { "foo": "bar" },
    "dedupe_key": "manual:abc123"
  }
  ```
- **201 Response** when inserted, **200** when skipped due to duplicate `dedupe_key`.

#### List Events
- **GET** `/worker/events`
- **Query**: `site_id`, `user_id`, `limit` (default 50, max 100)
- **200 Response**
  ```json
  {
    "events": [ { "id": 1, "site_id": "2f3...", ... } ],
    "count": 1
  }
  ```

---

## Error Envelope
All services standardize errors as:
```json
{
  "error": {
    "message": "human readable text",
    "status": 400
  }
}
```
