# ISUCOMA（イスコマ）

ISUCON 形式の競技用リポジトリ「ISUCOMA」。椅子専門のライブコマースサービスを題材にした
バックエンド API を、レギュレーションの範囲内でチューニングし、GMV（総取引額）ベースのスコアを
高めることを目指す。

## ドキュメント

- **アプリケーションマニュアル**: [docs/manual/application.md](docs/manual/application.md)
  （サービス概要・用語・API 仕様）
- **当日マニュアル**: [docs/manual/rules.md](docs/manual/rules.md)
  （競技形式・スコア・fail 条件・レギュレーション）

まずは上記2つのマニュアルに目を通すこと。

## リポジトリ構成

```
webapp/go/       リファレンス実装（Go）
webapp/sql/      スキーマ（schema.sql）・初期データ（init/seed.sql）
payment-mock/    決済モック（外部決済ゲートウェイのモックサーバー）
provisioning/    Docker Compose（compose.yaml）・nginx（nginx.conf）
docs/            マニュアル
```

## 環境の起動（Docker Compose）

MySQL 8 / nginx / Go アプリ / 決済モック / sweeper（カート期限切れ処理の定期実行）の
各コンテナを `docker compose` の単一コマンドで起動する。

```bash
cd provisioning
docker compose up --build -d   # 全サービスが healthy になるまで待つ
docker compose ps              # 全サービスが healthy であることを確認

curl -X POST http://localhost/api/initialize   # {"lang":"go"} が返ればOK

docker compose down -v         # 停止・後片付け（MySQLのデータボリュームも削除）
```

初回起動時、MySQL コンテナは `webapp/sql/schema.sql` と `webapp/sql/init/seed.sql` を
`docker-entrypoint-initdb.d` 経由で自動適用する。

### 環境変数（Go アプリ、`webapp/go`）

| 変数名 | 説明 | デフォルト |
|---|---|---|
| `ISUCOMA_DB_HOST` | MySQL ホスト名 | `127.0.0.1` |
| `ISUCOMA_DB_PORT` | MySQL ポート | `3306` |
| `ISUCOMA_DB_USER` | MySQL ユーザー | `isucoma` |
| `ISUCOMA_DB_PASSWORD` | MySQL パスワード | `isucoma` |
| `ISUCOMA_DB_NAME` | MySQL データベース名 | `isucoma` |
| `ISUCOMA_SQL_DIR` | `schema.sql` / `init/seed.sql` を配置したディレクトリ（`POST /api/initialize` が参照） | `/webapp/sql` |
| `ISUCOMA_SESSION_SECRET` | Cookie セッションの署名鍵。本番相当の運用では必ず上書きする | `isucoma-dev-secret-change-me`（開発用） |
| `ISUCOMA_LISTEN_PORT` | アプリの待受ポート | `8080` |

### seed アカウント（`webapp/sql/init/seed.sql`）

全アカウントのパスワードは `password` 固定（bcrypt でハッシュ化して格納）。

- Admin: `admin1@isucoma.test`
- セラー: `seller1@isucoma.test` 〜 `seller10@isucoma.test`
- 視聴者: `viewer1@isucoma.test` 〜 `viewer300@isucoma.test`

### ローカルでの Go アプリ単体ビルド

```bash
cd webapp/go
go build -mod=vendor ./...
go vet ./...
```

依存モジュールは `webapp/go/vendor/` にベンダリング済み。Docker ビルド（`webapp/go/Dockerfile`）は
この vendor ディレクトリを使い、ビルド専用コンテナの外部ネットワークアクセスを前提にせず
オフラインでビルドできる。

## ベンチマーカーの実行

ベンチマーカーは競技用ベンチ VM 上のポータルから実行する。手元の Docker Compose 環境に対して
自分でベンチを流す必要はない（当日マニュアル参照）。
