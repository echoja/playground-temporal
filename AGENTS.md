# Agents Overview

This playground exposes two cooperating Go services that simulate syncing data from an external e-commerce builder into an internal event log.

## Builder Service (`cmd/builder`)
- **Role**: Acts like the third-party site builder. Stores sites, storefront users, and orders inside `builder.db` (SQLite).
- **Public APIs**:
  - `POST /builder/sites` creates a site and returns its long-lived `access_key`.
  - `GET /builder/sites` lists available sites (for manual inspection).
  - `POST /builder/sites/{id}/random-user` and `POST /builder/sites/{id}/random-order` seed data for testing pagination.
  - `GET /builder/api/sites/{id}/users` and `/orders` expose paginated resources (10 items max per page) that require the `X-Access-Key` header.
- **Notes**: All admin endpoints are unauthenticated to simplify local development. The worker uses only the `/builder/api/...` paths.

## Worker Service (`cmd/worker`)
- **Role**: Receives site registrations, talks to the builder APIs, and appends deduplicated events into `events.db` (SQLite).
- **Key Endpoints**:
  - `POST /worker/sites` registers a site (validated by calling the builder profile endpoint).
  - `DELETE /worker/sites/{id}` revokes access.
  - `POST /worker/sites/{id}/sync/users` and `/sync/orders` traverse all builder pages, carry forward the latest `utm_source`, and insert `signup` / `order_created` events.
  - `POST /worker/events/random` seeds attribution events so future syncs can reuse UTM data.
  - `POST /worker/events` and `GET /worker/events` allow manual inspection and insertion.
- **Deduplication Logic**: Each event writes a `dedupe_key` (`signup:<site>:<user>` or `order:<site>:<order>`) and relies on `ON CONFLICT DO NOTHING` to keep the event log append-only without duplicates.

## Running Locally
1. Start the builder API: `go run ./cmd/builder --db builder.db --addr :8081`
2. Start the worker API: `go run ./cmd/worker --db events.db --addr :8082`
3. Create a site and register it with the worker, then trigger syncs. All endpoints are JSON-friendly for Postman or curl.

## Event Flow Summary
1. Builder seeds users/orders (single source of truth for commerce data).
2. Worker registration stores `access_key` and base URL.
3. Sync endpoints page across the builder API (10 items per page) and write `signup`/`order_created` events.
4. Before writing each event, the worker looks up the latest attribution (`utm_source`) for that `user_id` and carries it forward.

Both binaries log their bind address and underlying DB path on startup. The databases are auto-created if missing, so no migrations need to be run manually.
