# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26
ARG TSREADEX_REF=master-260428

# tsreadex は依存のない C++11。静的リンクして scratch に載せる。
FROM alpine:3.24 AS tsreadex
ARG TSREADEX_REF
RUN apk add --no-cache build-base git
RUN git clone --depth 1 --branch "${TSREADEX_REF}" https://github.com/xtne6f/tsreadex.git /src
WORKDIR /src
RUN make LDFLAGS="-static -s"

FROM golang:${GO_VERSION}-alpine AS proxy
# 上流へ送る User-Agent に埋め込むバージョン(リリースでは、タグから v を除いたもの)。
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/onodai145/mirakurun-tsreadex-proxy/internal/upstream.version=${VERSION}" -o /out/mirakurun-tsreadex-proxy ./cmd/mirakurun-tsreadex-proxy

FROM scratch
COPY --from=tsreadex /src/tsreadex /usr/local/bin/tsreadex
COPY --from=proxy /out/mirakurun-tsreadex-proxy /usr/local/bin/mirakurun-tsreadex-proxy
COPY --from=tsreadex /src/License.txt /usr/share/licenses/tsreadex/License.txt
COPY LICENSE /usr/share/licenses/mirakurun-tsreadex-proxy/LICENSE
ENV PATH=/usr/local/bin
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mirakurun-tsreadex-proxy"]
