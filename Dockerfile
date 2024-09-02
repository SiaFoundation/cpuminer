FROM docker.io/library/golang:1.23 AS builder

WORKDIR /cpuminerd

# Install dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Enable CGO for sqlite3 support
ENV CGO_ENABLED=1 

RUN go generate ./...
RUN go build -o bin/ -tags='netgo timetzdata' -trimpath -a -ldflags '-s -w'  ./cmd/cpuminerd

FROM scratch
LABEL maintainer="The Sia Foundation <info@sia.tech>" \
      org.opencontainers.image.description.vendor="The Sia Foundation" \
      org.opencontainers.image.description="A basic cpu miner for mining using a walletd node" \
      org.opencontainers.image.source="https://github.com/SiaFoundation/cpuminer" \
      org.opencontainers.image.licenses=MIT

ENV PUID=0
ENV PGID=0

# copy binary and prepare data dir.
COPY --from=builder /cpuminerd/bin/* /usr/bin/

USER ${PUID}:${PGID}

ENTRYPOINT [ "cpuminerd" ]