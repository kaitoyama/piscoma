-- ISUCOMA v1 スキーマ（一枚もの。マイグレーション管理は行わない）
-- 外部キー制約は張らない（ISUCON慣習。整合性はアプリケーション側の責務とする）
-- カテゴリ文字列は 'gaming' / 'zaisu' / 'office' / 'outdoor' の4種（商品カテゴリ・視聴者の嗜好カテゴリ共通）

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS coupon_claims;
DROP TABLE IF EXISTS coupons;
DROP TABLE IF EXISTS drops;
DROP TABLE IF EXISTS reactions;
DROP TABLE IF EXISTS live_viewers;
DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS carts;
DROP TABLE IF EXISTS stocks;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS lives;
DROP TABLE IF EXISTS user_icons;
DROP TABLE IF EXISTS users;

-- 視聴者・セラー・Admin 共通のアカウントテーブル
CREATE TABLE users (
    id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    email                VARCHAR(255)    NOT NULL,
    password_hash        VARCHAR(255)    NOT NULL,
    display_name         VARCHAR(64)     NOT NULL,
    role                 ENUM('viewer', 'seller', 'admin') NOT NULL,
    -- 視聴者のみが持つ嗜好カテゴリ（§3.1）。セラー/Admin は NULL
    preference_category  VARCHAR(32)     NULL,
    created_at           DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uq_users_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE sessions (
    token      CHAR(64)        NOT NULL,
    user_id    BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (token)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ユーザーアイコン（§4.11: 商品画像・アイコンはDB格納のBLOB配信から開始）
CREATE TABLE user_icons (
    user_id     BIGINT UNSIGNED NOT NULL,
    image       MEDIUMBLOB      NOT NULL,
    image_hash  CHAR(64)        NOT NULL,
    updated_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 配信（v1では負荷走行中は常に配信中。開始・終了の概念は持たない §2）
CREATE TABLE lives (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    seller_id           BIGINT UNSIGNED NOT NULL,
    title               VARCHAR(255)    NOT NULL,
    category            VARCHAR(32)     NOT NULL,
    -- 今紹介中の商品（§4.7）。配信開始直後などはまだ何もピン留めしていない場合があるので NULL 許容
    pinned_product_id   BIGINT UNSIGNED NULL,
    created_at          DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_lives_seller_id (seller_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 商品（通常商品・ドロップ商品共通。ドロップの目玉商品かどうかは drops テーブル側で管理）
CREATE TABLE products (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    seller_id    BIGINT UNSIGNED NOT NULL,
    live_id      BIGINT UNSIGNED NOT NULL,
    name         VARCHAR(255)    NOT NULL,
    description  TEXT            NOT NULL,
    category     VARCHAR(32)     NOT NULL,
    price        INT UNSIGNED    NOT NULL,
    image        MEDIUMBLOB      NOT NULL,
    image_hash   CHAR(64)        NOT NULL,
    created_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_products_live_id (live_id),
    KEY idx_products_seller_id (seller_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE stocks (
    product_id  BIGINT UNSIGNED NOT NULL,
    quantity    INT UNSIGNED    NOT NULL,
    PRIMARY KEY (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- カート（ZOZO式引き当て。TTL付き §4.2）
CREATE TABLE carts (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id     BIGINT UNSIGNED NOT NULL,
    product_id  BIGINT UNSIGNED NOT NULL,
    live_id     BIGINT UNSIGNED NOT NULL,
    status      ENUM('active', 'checked_out', 'expired', 'canceled') NOT NULL DEFAULT 'active',
    expires_at  DATETIME(3)     NOT NULL,
    created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_carts_user_id (user_id),
    KEY idx_carts_product_id (product_id),
    KEY idx_carts_status_expires_at (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 注文（チェックアウトが成立したカート。GMV = orders.price - orders.discount の合計 §2）
CREATE TABLE orders (
    id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    cart_id           BIGINT UNSIGNED NOT NULL,
    user_id           BIGINT UNSIGNED NOT NULL,
    product_id        BIGINT UNSIGNED NOT NULL,
    live_id           BIGINT UNSIGNED NOT NULL,
    price             INT UNSIGNED    NOT NULL,
    discount          INT UNSIGNED    NOT NULL DEFAULT 0,
    coupon_claim_id   BIGINT UNSIGNED NULL,
    -- チェックアウトのリトライに使う冪等キー（§4.5）
    idempotency_key   VARCHAR(64)     NOT NULL,
    created_at        DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uq_orders_idempotency_key (idempotency_key),
    KEY idx_orders_user_id (user_id),
    KEY idx_orders_live_id (live_id),
    KEY idx_orders_product_id (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE comments (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    live_id      BIGINT UNSIGNED NOT NULL,
    user_id      BIGINT UNSIGNED NOT NULL,
    body         TEXT            NOT NULL,
    reply_to_id  BIGINT UNSIGNED NULL,
    created_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE live_viewers (
    live_id      BIGINT UNSIGNED NOT NULL,
    user_id      BIGINT UNSIGNED NOT NULL,
    last_seen_at DATETIME(3)     NOT NULL,
    PRIMARY KEY (live_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- いいね（毎秒・視聴者数に比例した高頻度書き込み §4.9）
CREATE TABLE reactions (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    live_id     BIGINT UNSIGNED NOT NULL,
    user_id     BIGINT UNSIGNED NOT NULL,
    created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_reactions_live_id (live_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE drops (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    live_id        BIGINT UNSIGNED NOT NULL,
    product_id     BIGINT UNSIGNED NOT NULL,
    scheduled_at   DATETIME(3)     NOT NULL,
    announced      TINYINT(1)      NOT NULL DEFAULT 0,
    ends_at        DATETIME(3)     NOT NULL,
    status         ENUM('scheduled', 'active', 'sold_out', 'closed') NOT NULL DEFAULT 'scheduled',
    created_at     DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_drops_live_id (live_id),
    KEY idx_drops_product_id (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ライブ限定クーポン（固定額引き・先着m枚 §4.8）
CREATE TABLE coupons (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    live_id          BIGINT UNSIGNED NOT NULL,
    seller_id        BIGINT UNSIGNED NOT NULL,
    amount           INT UNSIGNED    NOT NULL,
    total_count      INT UNSIGNED    NOT NULL,
    remaining_count  INT UNSIGNED    NOT NULL,
    created_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_coupons_live_id (live_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- クーポン取得履歴（1人1枚 = coupon_id, user_id の UNIQUE 制約で担保）
CREATE TABLE coupon_claims (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    coupon_id      BIGINT UNSIGNED NOT NULL,
    user_id        BIGINT UNSIGNED NOT NULL,
    used_order_id  BIGINT UNSIGNED NULL,
    created_at     DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uq_coupon_claims_coupon_user (coupon_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 通知（ドロップ予告・開始、カート期限警告、チェックアウト結果、購入速報等 §4.6）
-- user_id が NULL の場合は配信内の全視聴者向けのブロードキャストを表す
CREATE TABLE notifications (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    live_id     BIGINT UNSIGNED NOT NULL,
    user_id     BIGINT UNSIGNED NULL,
    type        VARCHAR(32)     NOT NULL,
    payload     JSON            NOT NULL,
    created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_notifications_live_id (live_id),
    KEY idx_notifications_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE settings (
    name  VARCHAR(64)  NOT NULL,
    value VARCHAR(255) NOT NULL,
    PRIMARY KEY (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO settings (name, value) VALUES ('campaign_level', '1');

SET FOREIGN_KEY_CHECKS = 1;
