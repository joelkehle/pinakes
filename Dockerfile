FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/pinakes ./cmd/pinakes
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/pinakes-db-compact ./cmd/pinakes-db-compact

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/pinakes /usr/local/bin/pinakes
COPY --from=builder /out/pinakes-db-compact /usr/local/bin/pinakes-db-compact

WORKDIR /app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/pinakes"]
