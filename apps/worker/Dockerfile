FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /worker ./cmd/worker

FROM gcr.io/distroless/static AS runtime

COPY --from=builder /worker /worker

ENTRYPOINT ["/worker"]
