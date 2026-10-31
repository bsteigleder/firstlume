# One image for every component (server, agent and executor).
# The UI and the binary build on the builder's platform and cross-compile, so
# multi-platform images don't run the toolchains under emulation.
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/firstlume ./cmd/firstlume

FROM alpine:3.22
# git and ssh are needed by the executor to clone repositories and by
# Terraform/Terragrunt to fetch git-sourced modules.
RUN apk add --no-cache ca-certificates git openssh-client \
    && addgroup -g 65532 firstlume \
    && adduser -D -u 65532 -G firstlume -h /home/firstlume firstlume
COPY --from=build /out/firstlume /usr/local/bin/firstlume
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/firstlume"]
CMD ["server"]
