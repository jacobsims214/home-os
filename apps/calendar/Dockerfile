# Build context is the repository root (see release.yml and
# deploy/docker-compose.yml): the calendar module imports home-os/icalx via a
# replace directive pointing at ../../packages/icalx, which must be inside the
# context. GOWORK=off builds with go.mod's replace instead of the workspace,
# so only the two modules the binary needs are copied.
FROM golang:1.25-alpine AS builder
ENV GOWORK=off
WORKDIR /src/apps/calendar
COPY packages/icalx/go.mod packages/icalx/go.sum /src/packages/icalx/
COPY apps/calendar/go.mod apps/calendar/go.sum ./
RUN go mod download
COPY packages/icalx/ /src/packages/icalx/
COPY apps/calendar/ ./
RUN CGO_ENABLED=0 go build -o /calendar ./cmd/calendar

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /calendar /calendar
EXPOSE 8081
ENTRYPOINT ["/calendar"]
