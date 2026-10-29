# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26
ARG TSREADEX_REF=master-260428

# tsreadex は依存のない C++11。静的リンクして scratch に載せる。
FROM alpine:3.20 AS tsreadex
ARG TSREADEX_REF
RUN apk add --no-cache build-base git
RUN git clone --depth 1 --branch "${TSREADEX_REF}" https://github.com/xtne6f/tsreadex.git /src
WORKDIR /src
RUN make LDFLAGS="-static -s"

FROM golang:${GO_VERSION}-alpine AS proxy
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tsreadex-proxy ./cmd/tsreadex-proxy

FROM scratch
COPY --from=tsreadex /src/tsreadex /usr/local/bin/tsreadex
COPY --from=proxy /out/tsreadex-proxy /usr/local/bin/tsreadex-proxy
COPY --from=tsreadex /src/License.txt /usr/share/licenses/tsreadex/License.txt
COPY LICENSE /usr/share/licenses/tsreadex-proxy/LICENSE
ENV PATH=/usr/local/bin
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tsreadex-proxy"]
