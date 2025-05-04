FROM docker.io/golang:1.24-alpine3.21 as builder
WORKDIR /app

RUN apk add --no-cache gcc git musl-dev

COPY go.mod go.sum ./
RUN go mod download

COPY driver/ .
RUN go build -buildmode pie \
    -ldflags "\
    -linkmode external \
    -extldflags '-static' \
    -w -s" \
    -tags 'static_build' \
    -o cinder

####################

FROM docker.io/alpine:3.21

RUN apk add --no-cache e2fsprogs

COPY --from=builder /app/cinder /cinder
