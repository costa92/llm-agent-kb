# Stage 1: build
FROM golang:1.23 AS builder

WORKDIR /src

# Copy module files first for layer-cache efficiency.
COPY go.mod go.sum ./
RUN GOWORK=off go mod download

# Copy source.
COPY . .

# Build the kbd binary. GOWORK=off: standalone module, no umbrella go.work.
RUN GOWORK=off CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /kbd ./cmd/kbd

# Stage 2: minimal runtime image
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /kbd /kbd

EXPOSE 8080

ENTRYPOINT ["/kbd"]
