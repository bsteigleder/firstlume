# One image for every component (server, agent and executor).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/firstlume ./cmd/firstlume

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
