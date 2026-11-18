# Build context is the repository root (see release.yml): the mcp server
# imports home-os/allergen, home-os/icalx, home-os/billcal and home-os/billdue
# via replace directives pointing at ../../packages/*, which must be inside the
# context. GOWORK=off builds with go.mod's replace instead of the workspace, so
# only the modules the binary needs are copied. The root .dockerignore keeps
# local artefacts out of the context.
FROM golang:1.25-alpine AS builder
ENV GOWORK=off
WORKDIR /src/apps/mcp
COPY packages/allergen/go.mod packages/allergen/go.sum /src/packages/allergen/
COPY packages/icalx/go.mod packages/icalx/go.sum /src/packages/icalx/
COPY packages/billcal/go.mod packages/billcal/go.sum /src/packages/billcal/
COPY packages/billdue/go.mod /src/packages/billdue/
COPY apps/mcp/go.mod apps/mcp/go.sum ./
RUN go mod download
COPY packages/allergen/ /src/packages/allergen/
COPY packages/icalx/ /src/packages/icalx/
COPY packages/billcal/ /src/packages/billcal/
COPY packages/billdue/ /src/packages/billdue/
COPY apps/mcp/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp ./cmd/mcp

FROM gcr.io/distroless/static:nonroot AS runtime

COPY --from=builder /mcp /mcp

EXPOSE 8082

ENTRYPOINT ["/mcp"]
