FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.work go.work.sum ./
COPY apps/api/go.mod apps/api/go.sum ./apps/api/
COPY apps/worker/go.mod apps/worker/go.sum ./apps/worker/
COPY apps/calendar/go.mod apps/calendar/go.sum ./apps/calendar/
COPY apps/mcp/go.mod apps/mcp/go.sum ./apps/mcp/
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /worker ./apps/worker/cmd/worker

FROM gcr.io/distroless/static:nonroot AS runtime

COPY --from=builder /worker /worker

ENTRYPOINT ["/worker"]
