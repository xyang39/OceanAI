FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build /out/server ./server
USER app
EXPOSE 8080
CMD ["/app/server"]
