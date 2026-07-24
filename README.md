# Home OS

A family-first household operating system — a unified application for managing a household. Home OS provides a simple, searchable interface for family calendar, multi-property management, asset inventory, maintenance tracking, bill tracking, vendor management, vehicle management, pet management, file/document management with OCR, native secrets management with client-side encryption, and smart-home context via Home Assistant.

## Quick Start

```bash
git clone https://github.com/jacobsims214/home-os.git && cd home-os
docker compose -f deploy/docker-compose.yml up -d --build
```

Open **http://localhost:3000** and log in with:
- **Email:** `admin@homeos.demo`
- **Password:** `demo1234`

Demo data is seeded automatically on first startup.

## Repo Structure

```
home-os/
├── apps/
│   ├── api/          # Go core API (modular monolith, chi + pgx/v5)
│   ├── calendar/     # Go CalDAV service (RFC 5545)
│   ├── worker/       # Go async worker (Proto.Actor, Tika OCR, Typesense)
│   └── ui/           # Next.js 14 PWA (Mantine v7, TanStack Query, Zustand)
├── packages/
│   └── db/           # 28 golang-migrate migrations (001-028)
├── deploy/
│   ├── docker-compose.yml   # Full dev stack (8 services + Caddy proxy)
│   ├── Caddyfile            # Reverse proxy with optional HTTPS
│   └── helm/
│       └── home-os/         # Production Helm chart (17 resources)
├── .github/
│   └── workflows/           # CI: release builds + PR checks
└── .amp.json
```

## Tech Stack

| Layer | Technology |
|---|---|
| Frontend | Next.js 14 + React 18 + TypeScript + Mantine v7 + Tailwind CSS |
| Backend | Go 1.25 (chi router, pgx/v5, golang-jwt) |
| Calendar | Go CalDAV service (RFC 5545, PROPFIND, REPORT, sync-collection) |
| Database | PostgreSQL 16 (28 migrations via golang-migrate) |
| Search | Typesense 27 (outbox-driven indexing) |
| Async | Proto.Actor (file processing, OCR, search indexing) |
| File Storage | Native PostgreSQL bytea + Tika OCR |
| Secrets | Zero-knowledge AES-256-GCM (Web Crypto API) |
| Proxy | Envoy (production) / Caddy (dev, optional HTTPS) |

## Services & Ports

| Service | Port | Notes |
|---|---|---|
| UI (Next.js) | 3000 | PWA with BFF session layer |
| API (Go) | 8080 | REST API (all CRUD + search) |
| Calendar (Go) | 8081 | CalDAV for Apple Calendar |
| PostgreSQL | 5433 | Data + file blobs + secrets |
| Typesense | 8109 | Full-text search |
| Tika | 9998 | OCR / text extraction |
| Caddy | 80/443 | Reverse proxy (dev) |
| MinIO | 9002 | Legacy (no longer used by app) |

## Development

All services run in Docker Compose with hot-reload:

```bash
# Start everything
docker compose -f deploy/docker-compose.yml up -d --build

# View logs
docker compose -f deploy/docker-compose.yml logs -f api
docker compose -f deploy/docker-compose.yml logs -f ui

# Restart a single service
docker compose -f deploy/docker-compose.yml restart api

# Stop everything
docker compose -f deploy/docker-compose.yml down
```

## Apple Calendar (CalDAV)

Home OS includes a full CalDAV server compatible with Apple Calendar:

1. Log in to the app at `http://localhost:3000`
2. Go to **Settings → Calendar Sync → Generate App Password**
3. Add the account in Apple Calendar:
   - **Server:** `http://localhost:8081` (or `https://localhost:8443` via Caddy)
   - **Username:** `admin@homeos.demo`
   - **Password:** (the generated password)

Your calendars (Johnson Family, Main Residence, Lake Cabin) sync automatically.

## Demo Mode

`DEMO_MODE=true` seeds the database on first startup with:
- Demo user: `admin@homeos.demo` / `demo1234`
- The Johnson Family household with 2 properties, 15 assets, 5 maintenance tasks, 3 vehicles, 2 pets, 4 vendors, 5 bills, 3 calendars, and 69 calendar events

The seed is idempotent — safe to restart without duplicating data.

## Production Deployment

### Helm Chart

A production Helm chart is available at `deploy/helm/home-os/`:

```bash
helm install home-os ./deploy/helm/home-os \
  --set secrets.jwtSecret="..." \
  --set secrets.typesenseApiKey="..." \
  --set secrets.encryptionKey="..."
```

The chart includes 17 resources:
- **Envoy** — front door LoadBalancer, routes by path (/api/* → api, /dav/* → calendar, /* → ui)
- **CloudNativePG** — PostgreSQL 16 cluster with 2 replicas, 10Gi storage
- **Deployments** — api, calendar, worker, ui, tika (all with resource limits)
- **StatefulSet** — Typesense with persistent volume
- **Job** — Migrations (Helm hook, runs before deployments)

### GitHub Container Registry

Images are built automatically on release via GitHub Actions:

```bash
ghcr.io/jacobsims214/home-os/api:latest
ghcr.io/jacobsims214/home-os/calendar:latest
ghcr.io/jacobsims214/home-os/worker:latest
ghcr.io/jacobsims214/home-os/ui:latest
ghcr.io/jacobsims214/home-os/migrations:latest
```

## External Integrations

Home Assistant is NOT included in the compose stack. Configure it via environment variables if you have an existing instance. Paperless-ngx and Vaultwarden integrations were removed in favor of native file storage and secrets management.

## Architecture

For detailed architecture documentation, see the KB docs in the repo:

- `architecture/project-overview.md` — What Home OS is and the full tech stack
- `architecture/api-architecture.md` — Go API structure and routes
- `architecture/domain-model.md` — Entity definitions and relationships
- `architecture/database-schema.md` — Full PostgreSQL schema
- `architecture/infrastructure.md` — Docker Compose and Helm deployment