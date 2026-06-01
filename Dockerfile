# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Download modules first, in their own layer, so source-only changes don't
# re-download dependencies on every rebuild.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO disabled produces a static binary that runs in a minimal final image.
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/listings-api ./cmd/server

# ---- run stage ----
FROM alpine:3.20
# Run as a non-root user rather than root.
RUN adduser -D -u 10001 appuser
COPY --from=build /bin/listings-api /usr/local/bin/listings-api
USER appuser
EXPOSE 8080
ENTRYPOINT ["listings-api"]
