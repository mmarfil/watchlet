FROM golang:1.22-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/watchlet ./cmd/watchlet

FROM alpine:3.22

RUN apk add --no-cache ca-certificates docker-cli docker-cli-compose

COPY --from=build /out/watchlet /usr/local/bin/watchlet

ENTRYPOINT ["watchlet"]
