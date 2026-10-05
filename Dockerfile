# syntax=docker/dockerfile:1

# ---- dashboard ----
FROM --platform=$BUILDPLATFORM node:26-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build

# ---- binary ----
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kube-bmc ./cmd/kube-bmc

# ---- runtime: ipmitool is the only dependency (used by the agent) ----
FROM alpine:3.22
RUN apk add --no-cache ipmitool ca-certificates tzdata
COPY --from=build /out/kube-bmc /usr/local/bin/kube-bmc
ENTRYPOINT ["kube-bmc"]
