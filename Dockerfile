FROM golang:1.24-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go import.go appearance.go ./
COPY web ./web
RUN CGO_ENABLED=1 GOAMD64=v1 go build -trimpath -ldflags="-s -w" -o /homemenu .

FROM alpine:3
RUN mkdir /data && chown 65532:65532 /data
COPY --from=build /homemenu /homemenu
USER 65532:65532
ENV DATA_DIR=/data GOMEMLIMIT=24MiB
VOLUME ["/data"]
EXPOSE 5000
ENTRYPOINT ["/homemenu"]
