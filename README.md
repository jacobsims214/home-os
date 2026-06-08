# Home OS

A family-first household operating system — a unified application that becomes the primary interface for managing a household. Home OS integrates with existing best-of-breed tools (Home Assistant, Paperless-ngx, Vaultwarden, MinIO) to provide a simple, searchable interface for family calendar, multi-property management, asset inventory, maintenance tracking, bill tracking, vendor management, vehicle management, pet management, and smart-home context. Any family member should be able to find needed household information within 10 seconds.

## Quick Start

```bash
git clone <repo-url> && cd home-os
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
│   ├── api/          # Go core API (modular monolith)
│   ├── calendar/     # Go CalDAV service
│   ├── worker/       # Go async worker (Proto.Actor)
│   └── ui/           # Next.js PWA + BFF
├── packages/
│   └── db/           # Shared DB migrations (golang-migrate)
├── deploy/
│   ├── docker-compose.yml   # Single-file dev stack (all services)
│   └── helm/
│       └── home-os/         # Helm chart for production
└── .amp.json
```

## Tech Stack

| Layer | Technology |
|---|---|
| Frontend | Next.js 14 + React 18 + TypeScript + Tailwind CSS (PWA) |
| Backend | Go 1.22+ (modular monolith, chi router, pgx/v5) |
| Calendar | Go CalDAV service (RFC 5545) |
| Database | PostgreSQL 16 |
| Search | Typesense 27 |
| Async | Proto.Actor |
| Storage | MinIO |
| Docs | Paperless-ngx (external) |
| Secrets | Vaultwarden (external) |

## Services & Ports

| Service | Port | Notes |
|---|---|---|
| UI (Next.js) | 3000 | Main app |
| API (Go) | 8080 | REST API |
| Calendar (Go) | 8081 | CalDAV |
| PostgreSQL | 5433 | Remapped to avoid conflicts |
| Typesense | 8109 | Remapped to avoid conflicts |
| MinIO API | 9002 | Remapped to avoid conflicts |
| MinIO Console | 9003 | Web UI |

## Prerequisites

- **Docker** and Docker Compose v2

## Development

All services run in Docker Compose. Source directories are mounted for hot-reload:
- Go services: `go run` recompiles on file change
- Next.js: `npm run dev` with Fast Refresh

```bash
# Start everything
docker compose -f deploy/docker-compose.yml up -d --build

# View logs
docker compose -f deploy/docker-compose.yml logs -f api
docker compose -f deploy/docker-compose.yml logs -f ui

# Restart a single service after code changes
docker compose -f deploy/docker-compose.yml restart api

# Stop everything
docker compose -f deploy/docker-compose.yml down
```

## Demo Mode

`DEMO_MODE=true` is set by default in the compose file. On first startup, the API seeds:
- Demo user: `admin@homeos.demo` / `demo1234`
- The Johnson Family household with 2 properties, 15 assets, 5 maintenance tasks, 3 vehicles, 2 pets, 4 vendors, 5 bills

The seed is idempotent — safe to restart without duplicating data.

## External Integrations (Optional)

Paperless-ngx, Vaultwarden, and Home Assistant are NOT included in the compose stack. Configure them via environment variables in `deploy/docker-compose.yml` if you have existing instances.

## Architecture

For detailed architecture documentation, see the AMP knowledge base:

- `architecture/project-overview.md` — What Home OS is and the full tech stack
- `architecture/api-architecture.md` — Go API structure and routes
- `architecture/domain-model.md` — Entity definitions and relationships
- `architecture/database-schema.md` — Full PostgreSQL schema
- `architecture/infrastructure.md` — Docker Compose and Helm deployment
