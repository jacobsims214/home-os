# Build context is the repository root (see release.yml and
# deploy/docker-compose.yml): the worker imports home-os/api (smtp), home-os/icalx,
# home-os/billcal and home-os/billdue via replace directives pointing at ../api
# and ../../packages/*, which must be inside the context. GOWORK=off builds
# with go.mod's replaces instead of the workspace, so only the modules
# the binary needs are copied.
FROM golang:1.25-alpine AS builder
ENV GOWORK=off
WORKDIR /src/apps/worker
COPY packages/icalx/go.mod packages/icalx/go.sum /src/packages/icalx/
COPY packages/billcal/go.mod packages/billcal/go.sum /src/packages/billcal/
COPY packages/billdue/go.mod packages/billdue/go.sum /src/packages/billdue/
COPY packages/notify/go.mod /src/packages/notify/
COPY apps/api/go.mod apps/api/go.sum /src/apps/api/
COPY apps/worker/go.mod apps/worker/go.sum ./
RUN go mod download
COPY packages/icalx/ /src/packages/icalx/
COPY packages/billcal/ /src/packages/billcal/
COPY packages/billdue/ /src/packages/billdue/
COPY packages/notify/ /src/packages/notify/
COPY apps/api/ /src/apps/api/
COPY apps/worker/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /worker ./cmd/worker

FROM gcr.io/distroless/static:nonroot AS runtime
COPY --from=builder /worker /worker
ENTRYPOINT ["/worker"]
