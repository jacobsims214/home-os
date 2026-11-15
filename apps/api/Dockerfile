# Build context is the repository root (see release.yml and
# deploy/docker-compose.yml): the api imports home-os/icalx via a replace
# directive pointing at ../../packages/icalx, which must be inside the
# context. GOWORK=off builds with go.mod's replace instead of the workspace,
# so only the two modules the binary needs are copied. The root
# .dockerignore keeps local artefacts out of the context.
FROM golang:1.25-alpine AS builder
ENV GOWORK=off
WORKDIR /src/apps/api
COPY packages/icalx/go.mod packages/icalx/go.sum /src/packages/icalx/
COPY apps/api/go.mod apps/api/go.sum ./
RUN go mod download
COPY packages/icalx/ /src/packages/icalx/
COPY apps/api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /api ./cmd/api

FROM gcr.io/distroless/static:nonroot AS runtime

COPY --from=builder /api /api

EXPOSE 8080

ENTRYPOINT ["/api"]
