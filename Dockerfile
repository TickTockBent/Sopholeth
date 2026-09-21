FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o server ./cmd/server

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
RUN adduser -D -s /bin/sh node
WORKDIR /app
COPY --from=builder /app/server .
RUN chown -R node:node /app
USER node
EXPOSE 8080
CMD ["./server"]
