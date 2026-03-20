FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -o /bin/scheduler ./cmd/scheduler
RUN CGO_ENABLED=0 go build -o /bin/worker ./cmd/worker
RUN CGO_ENABLED=0 go build -o /bin/forgectl ./cmd/forgectl

FROM alpine:3.19

RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/scheduler /usr/local/bin/scheduler
COPY --from=builder /bin/worker /usr/local/bin/worker
COPY --from=builder /bin/forgectl /usr/local/bin/forgectl
