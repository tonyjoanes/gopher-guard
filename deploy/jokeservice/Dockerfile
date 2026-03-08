# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/jokeservice ./cmd/jokeservice

# ---- runtime stage ----
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/jokeservice /jokeservice

EXPOSE 8080

ENTRYPOINT ["/jokeservice"]
