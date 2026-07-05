# ISUCOMA Go リファレンス実装のマルチステージビルド
# ビルドコンテキストはリポジトリルート想定（provisioning/compose.yaml から
# `context: ..` / `dockerfile: webapp/go/Dockerfile` で参照される）。
# webapp/sql を同じイメージに焼き込み、ISUCOMA_SQL_DIR のデフォルト値 /webapp/sql と
# パスを一致させる（POST /api/initialize がここから初期データを読み込む）。

FROM golang:1.24 AS build
WORKDIR /src

# 依存モジュールは webapp/go/vendor/ にベンダリング済み（`go mod vendor` で生成）。
# ビルド専用コンテナからの外部ネットワークアクセスを前提にしないため、
# `go mod download` は行わず -mod=vendor でオフラインビルドする。
COPY webapp/go/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /out/isucoma-webapp .

FROM debian:bookworm-slim
# 注: このイメージはビルド時にパッケージマネージャでの追加インストールを行わない
# （CGO_ENABLED=0 の静的バイナリのみで完結させ、ビルド環境のネットワーク事情に
# 依存しないようにするため）。ヘルスチェックはバイナリ自身の `healthcheck` サブ
# コマンド（main.go 参照）で完結させ、curl/wget を container に足さずに済ませている。

COPY --from=build /out/isucoma-webapp /usr/local/bin/isucoma-webapp
COPY webapp/sql /webapp/sql

ENV ISUCOMA_SQL_DIR=/webapp/sql
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/isucoma-webapp"]
