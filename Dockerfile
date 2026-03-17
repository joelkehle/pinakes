FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/agent-bus ./cmd/agent-bus

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/agent-bus /usr/local/bin/agent-bus

WORKDIR /app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/agent-bus"]
