
# Home OS - Master Engineering Handoff v1

## Status

This document represents the authoritative architectural direction agreed upon during initial planning.

Audience:
- Engineering Leads
- Planning Agents
- Architecture Agents
- Story Generation Agents
- Implementation Teams

---

# Executive Summary

Home OS is a family-first household operating system.

The purpose is to provide a unified application that becomes the primary interface for managing a household while integrating with existing best-of-breed systems.

Home OS is NOT:

- A Home Assistant replacement
- A password manager
- A document management system
- A finance application
- A generic ERP

Home OS IS:

A family operating system that provides a simple interface for:

- Family calendar
- Multi-property management
- Asset inventory
- Maintenance tracking
- Bill tracking
- Household knowledge
- Vendor management
- Vehicle management
- Pet management
- Household search
- Document lookup
- Credential references
- Smart-home context

The primary UX goal:

Any family member should be able to find needed household information within 10 seconds.

---

# Final Technology Decisions

## Frontend

Stack:

- Next.js
- React
- TypeScript
- Tailwind
- PWA
- FullCalendar

Reasoning:

- Mobile-first
- Tablet friendly
- Installable on iOS
- Strong ecosystem
- Mature calendar support

---

## Core Backend

Stack:

- Go

Architecture:

- Modular monolith initially
- Domain-driven modules
- Service boundaries maintained internally

Reasoning:

- Team preference
- Performance
- Operational simplicity
- Strong Kubernetes support

---

## Calendar Technology

Decision:

Calendar is its own service.

Stack:

- Go Calendar Service
- CalDAV support
- RFC5545
- iCalendar storage
- RRULE support
- FullCalendar UI

Required Features:

- Family calendars
- Shared calendars
- Multi-property calendars
- Maintenance calendars
- Bills calendars
- Recurring events
- Event exceptions
- Apple Calendar compatibility
- iPhone compatibility

Routes:

/dav/*
/.well-known/caldav
/.well-known/carddav

---

## Database

Stack:

- PostgreSQL
- CloudNativePG

Source of truth:

Postgres

Everything important ultimately lives in Postgres.

---

## Search

Stack:

- Typesense

Reasoning:

- Vector search
- Semantic search
- Hybrid search
- Kubernetes friendly

Ownership:

Typesense is NEVER source of truth.

Typesense indexes Postgres.

---

## Documents

Stack:

- Existing Paperless-ngx

Responsibilities:

- OCR
- Document storage
- Document archival

Home OS stores:

- References
- Relationships
- Permissions
- Context

---

## Secrets

Stack:

- Existing Vaultwarden

Rules:

- Home OS stores references only
- Home OS never stores passwords

---

## Smart Home

Stack:

- Existing Home Assistant

Rules:

- Home Assistant owns devices
- Home Assistant owns automations
- Home OS consumes context

---

## Storage

Stack:

- Existing MinIO

Used for:

- Uploads
- Images
- Receipts
- Attachments

---

## Async Runtime

Stack:

- Proto.Actor

Used selectively.

Not used for CRUD.

---

# High-Level Architecture

User

-> Next.js PWA

-> Next.js BFF

-> Go Core API

Go Core API

-> Calendar Service
-> Postgres
-> Typesense
-> Home Assistant
-> Vaultwarden
-> Paperless
-> MinIO

---

# Kubernetes Topology

Namespace:

home-os

Deployments:

- home-os-ui
- home-os-api
- home-os-calendar
- home-os-worker

StatefulSets:

- typesense

Jobs:

- migrations
- indexing
- bootstrap

Ingress:

/
/api/*
/dav/*
/.well-known/*

External Dependencies:

- CloudNativePG Operator
- Paperless
- Vaultwarden
- Home Assistant
- MinIO

---

# Helm Design

Chart:

home-os

The chart SHALL:

Deploy:

- UI
- API
- Calendar
- Worker
- Typesense

Create:

- CloudNativePG Cluster CR (managed mode)

Support:

managed mode
external mode

Managed Mode:

Home OS creates:

- Cluster CR
- Database
- User
- Backups

External Mode:

Consumes existing database.

---

# Domain Model

Household

Contains:

- Members
- Properties
- Calendars
- Vehicles
- Pets
- Services

Property

Contains:

- Rooms
- Assets
- Vendors
- Maintenance
- Utilities
- Documents

Asset

Examples:

- HVAC
- Refrigerator
- Motorcycle
- Server
- TV

Asset Tracks:

- Purchase Date
- Purchase Price
- Warranty
- Serial Number
- Documents
- Maintenance

Entities:

households
users
memberships
properties
rooms
assets
documents
vendors
vehicles
pets
bills
maintenance_schedules
maintenance_tasks
calendars
calendar_objects
notifications
integrations
service_accounts
outbox_events

---

# Search Design

Primary UX Feature

One search box.

Examples:

wifi
insurance
vet
motorcycle title
hvac filter
garage

Indexed Objects:

- assets
- documents
- vendors
- bills
- maintenance
- notes
- vehicles
- pets
- properties
- calendar events

Collection:

household_search

Index Updates:

Outbox -> Worker -> Typesense

---

# Integration Framework

Core API is the integration router.

Integrations expose:

Health()
Validate()
Sync()
Disconnect()
Capabilities()

Capability Examples:

documents.read
documents.write
calendar.import
calendar.export
storage.write
homeassistant.read
secrets.reference

Supported Integrations:

Paperless
Vaultwarden
Home Assistant
MinIO

Future:

Google Calendar
Microsoft Calendar

---

# Actor Model Strategy

Proto.Actor is approved.

Good Actor Use Cases:

NotificationActor
IntegrationActor
CalendarSyncActor
MaintenanceActor
JobActor

Responsibilities:

Retries
Scheduling
Backoff
State Machines
Long Running Workflows

Do NOT use actors for:

CRUD
Authentication
Search

Persistence:

All important state persists to Postgres.

---

# Eventing Strategy

Use Transactional Outbox.

Write Flow:

API Transaction

1 Save Data
2 Save Outbox Event

Worker

1 Read Event
2 Dispatch Actor
3 Update Result

Events:

asset.created
asset.updated
document.linked
calendar.updated
maintenance.generated
bill.due
notification.sent

---

# Security Model

Roles

Owner

Family Manager

Family Member

Viewer

Housesitter

Vendor

Housesitter Access:

- WiFi
- Pet Instructions
- Emergency Contacts

No Access:

- Financials
- Password References
- Sensitive Documents

---

# Calendar Vision

Calendar is a first-class feature.

Calendars:

Family
Bills
Maintenance
Vehicles
Properties

Requirements:

Apple Calendar Support
CalDAV Support
Recurring Events
Exceptions
Shared Calendars

Storage:

Structured metadata + canonical iCalendar object storage.

---

# MVP Scope

Required

Households
Properties
Assets
Maintenance
Documents
Search
Family Calendar
CalDAV
Paperless Integration
Vaultwarden Integration
MinIO Uploads
Typesense
Proto.Actor Runtime
Helm Deployment

Deferred

Advanced Finance
AI Extraction
Vendor Portal
CardDAV
Mobile Push

---

# Planning Epics

Epic 1 Platform Foundation

Epic 2 Domain API

Epic 3 Family PWA

Epic 4 Calendar Service

Epic 5 Search Platform

Epic 6 Integration Framework

Epic 7 Paperless

Epic 8 Vaultwarden

Epic 9 Actor Runtime

Epic 10 Home Assistant

---

# Final Guidance To Engineering Teams

The most important design principle is simplicity for the family.

The household should experience:

"Everything about our home lives here."

while Home OS quietly orchestrates:

- Postgres
- Typesense
- Paperless
- Vaultwarden
- Home Assistant
- MinIO
- Calendar Services

without exposing the complexity of those systems.
