# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/agentchat ./cmd/agentchat

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agentchat /usr/local/bin/agentchat
USER nonroot:nonroot
VOLUME ["/data"]
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/agentchat"]
CMD ["hub", "--listen", "0.0.0.0:8787", "--data", "/data"]
