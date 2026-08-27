FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
WORKDIR /src

RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp ./cmd/mcp

FROM gcr.io/distroless/static:nonroot AS runtime

COPY --from=builder /mcp /mcp

EXPOSE 8082

ENTRYPOINT ["/mcp"]