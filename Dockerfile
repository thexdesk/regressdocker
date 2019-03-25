# syntax = docker/dockerfile:experimental
FROM golang:1.12.1-alpine3.9@sha256:5f7781ceb97dd23c28f603c389d71a0ce98f9f6c78aa8cbd12b6ca836bfc6c6c AS go
RUN apk add -U git gcc libc-dev

FROM go AS mod
WORKDIR /src
COPY go.mod .
COPY go.sum .
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=$GOPATH/pkg/mod go mod download

FROM go AS build
COPY --from=mod /root/.cache/go-build /root/.cache/go-build
COPY --from=mod $GOPATH/pkg/mod $GOPATH/pkg/mod
WORKDIR /src
COPY . .
RUN go build ./cmd/stress

FROM docker:18.09.3-dind@sha256:ec353956a21300964a7eb2b620a742c2730f618f4df35f60609b30969cd83ce8 AS slow
COPY --from=build /src/stress /usr/bin/stress

FROM docker:18.06.3-dind@sha256:dd0b6cc8d6951eb2f0843ab586af59c23ec3b743bc0bf8a49154af46894b8b89 AS fast
COPY --from=build /src/stress /usr/bin/stress
