
FROM golang:1.24 AS build
WORKDIR /src

COPY payment-mock/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/payment-mock .

FROM debian:bookworm-slim
# webapp/go/Dockerfile と同様、パッケージマネージャでの追加インストールは行わない。
# ヘルスチェックはバイナリ自身の `healthcheck` サブコマンド（main.go 参照）で完結させる。

COPY --from=build /out/payment-mock /usr/local/bin/payment-mock

EXPOSE 5000

ENTRYPOINT ["/usr/local/bin/payment-mock"]
