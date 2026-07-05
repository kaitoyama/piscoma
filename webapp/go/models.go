package main

import "time"

// ロール定数（users.role の ENUM 値と一致させる）
const (
	RoleViewer = "viewer"
	RoleSeller = "seller"
	RoleAdmin  = "admin"
)

// User は users テーブルの行を表す。
type User struct {
	ID                 int64     `db:"id" json:"id"`
	Email              string    `db:"email" json:"email"`
	PasswordHash       string    `db:"password_hash" json:"-"`
	DisplayName        string    `db:"display_name" json:"display_name"`
	Role               string    `db:"role" json:"role"`
	PreferenceCategory *string   `db:"preference_category" json:"preference_category,omitempty"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
}

// 嗜好カテゴリ・商品カテゴリの4種（仕様書 §2, §3.1）
var validCategories = map[string]bool{
	"gaming":  true,
	"zaisu":   true,
	"office":  true,
	"outdoor": true,
}

// liveRow は lives テーブルの行そのもの（DB スキャン専用の内部表現）。
type liveRow struct {
	ID              int64     `db:"id"`
	SellerID        int64     `db:"seller_id"`
	Title           string    `db:"title"`
	Category        string    `db:"category"`
	PinnedProductID *int64    `db:"pinned_product_id"`
	CreatedAt       time.Time `db:"created_at"`
}

// productRow は products テーブルの行そのもの（DB スキャン専用の内部表現）。
type productRow struct {
	ID          int64  `db:"id"`
	Name        string `db:"name"`
	Description string `db:"description"`
	Category    string `db:"category"`
	Price       uint32 `db:"price"`
}

// commentRow はコメント一覧取得時の JOIN 結果（users とのJOINで表示名だけを引く。
// §5.4: email 等の個人情報をここで取得しないことでレスポンスへの混入を防ぐ）。
type commentRow struct {
	ID              int64     `db:"id"`
	Body            string    `db:"body"`
	ReplyToID       *int64    `db:"reply_to_id"`
	CreatedAt       time.Time `db:"created_at"`
	UserID          int64     `db:"user_id"`
	UserDisplayName string    `db:"user_display_name"`
}

// Seller はレスポンスに含めるセラー情報（id・表示名のみ。§5.4）。
type Seller struct {
	ID          int64  `db:"id" json:"id"`
	DisplayName string `db:"display_name" json:"display_name"`
}

// Stats は配信の統計（視聴者数・いいね数）。
type Stats struct {
	ViewerCount   int64 `json:"viewer_count"`
	ReactionCount int64 `json:"reaction_count"`
}

type FeedLive struct {
	ID       int64          `json:"id"`
	Title    string         `json:"title"`
	Category string         `json:"category"`
	Seller   Seller         `json:"seller"`
	Stats    Stats          `json:"stats"`
	Products []ShelfProduct `json:"products"`
}

// PinnedProduct は配信詳細に含める「今紹介中の商品」（§4.7）。
type PinnedProduct struct {
	ID    int64  `db:"id" json:"id"`
	Name  string `db:"name" json:"name"`
	Price uint32 `db:"price" json:"price"`
}

// LiveDetail は GET /api/lives/:id のレスポンス（live-detail スペック「詳細の取得」）。
type LiveDetail struct {
	ID            int64          `json:"id"`
	Title         string         `json:"title"`
	Category      string         `json:"category"`
	Seller        Seller         `json:"seller"`
	PinnedProduct *PinnedProduct `json:"pinned_product"`
	Stats         Stats          `json:"stats"`
}

// CommentUser はコメントのレスポンスに含めるユーザー情報。
// §5.4: id と display_name のみ。email 等の個人情報を含めてはならない。
type CommentUser struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
}

// Comment は GET/POST /api/lives/:id/comments のレスポンス要素
// （live-comments スペック「一覧の取得」「コメント投稿と返信」）。
type Comment struct {
	ID        int64       `json:"id"`
	User      CommentUser `json:"user"`
	Body      string      `json:"body"`
	ReplyToID *int64      `json:"reply_to_id"`
	CreatedAt time.Time   `json:"created_at"`
}

// ShelfProduct は GET /api/lives/:id/products の1要素
// （live-shelf スペック「棚の取得」）。
type ShelfProduct struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Category       string `json:"category"`
	Price          uint32 `json:"price"`
	RemainingStock uint32 `json:"remaining_stock"`
}

// CartCreateResponse は POST /api/products/:id/cart の201レスポンス
// （cart-reservation スペック「カート投入による在庫引き当て」。expires_at は
// ミリ秒精度のRFC3339絶対時刻。§7.3: ベンチは expires_at − 受信時刻 = TTL ± 1秒 を検証する）。
type CartCreateResponse struct {
	CartID      int64  `json:"cart_id"`
	ExpiresAt   string `json:"expires_at"`
	RemainingMs int64  `json:"remaining_ms"`
}

type CartStatusResponse struct {
	CartID      int64  `json:"cart_id"`
	ProductID   int64  `json:"product_id"`
	RemainingMs int64  `json:"remaining_ms"`
	Status      string `json:"status"`
}

// CheckoutResponse は POST /api/carts/:id/checkout の200レスポンス
// （checkout スペック「チェックアウトの成立」「クーポンの適用」。coupon_id 指定時は
// discount = min(price, amount)、total = price - discount が入る。指定なしは discount=0）。
type CheckoutResponse struct {
	OrderID   int64  `json:"order_id"`
	ProductID int64  `json:"product_id"`
	Price     uint32 `json:"price"`
	Discount  uint32 `json:"discount"`
	Total     uint32 `json:"total"`
}

// OrderProduct は GET /api/orders の各要素に含める商品情報（id, name のみ）。
type OrderProduct struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// OrderListItem は GET /api/orders の1要素（orders スペック「自分の注文履歴」）。
type OrderListItem struct {
	OrderID   int64        `json:"order_id"`
	Product   OrderProduct `json:"product"`
	Price     uint32       `json:"price"`
	Discount  uint32       `json:"discount"`
	Total     uint32       `json:"total"`
	CreatedAt time.Time    `json:"created_at"`
}

// SellerSalesProduct は GET /api/seller/sales の by_product の1要素
// （seller-sales スペック「都度JOIN集計」。注文0の商品も sold_count=0 で含む）。
type SellerSalesProduct struct {
	ProductID int64  `json:"product_id"`
	Name      string `json:"name"`
	SoldCount int64  `json:"sold_count"`
	GMV       int64  `json:"gmv"`
}

// SellerSalesResponse は GET /api/seller/sales のレスポンス。
type SellerSalesResponse struct {
	TotalGMV   int64                `json:"total_gmv"`
	OrderCount int64                `json:"order_count"`
	ByProduct  []SellerSalesProduct `json:"by_product"`
}

// SellerProductCreateResponse は POST /api/seller/products の201レスポンス
// （seller-products スペック「商品の追加と在庫投入」）。
type SellerProductCreateResponse struct {
	ProductID int64 `json:"product_id"`
}

// SellerDropCreateResponse は POST /api/seller/drops の201レスポンス
// （drops スペック「ドロップの登録」。starts_at/ends_at はミリ秒精度のRFC3339絶対時刻）。
type SellerDropCreateResponse struct {
	DropID    int64  `json:"drop_id"`
	ProductID int64  `json:"product_id"`
	StartsAt  string `json:"starts_at"`
	EndsAt    string `json:"ends_at"`
}

// AdminRankingEntry は GET /api/admin/rankings の1要素（admin-analytics スペック
// 「セラー売上ランキング」。注文のないセラーも total_gmv=0 で含む）。
type AdminRankingEntry struct {
	SellerID    int64  `json:"seller_id"`
	DisplayName string `json:"display_name"`
	TotalGMV    int64  `json:"total_gmv"`
	OrderCount  int64  `json:"order_count"`
}

// ProductSearchItem は GET /api/admin/products/search の products の1要素
// （admin-analytics スペック「商品検索」）。
type ProductSearchItem struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Price    uint32 `json:"price"`
	SellerID int64  `json:"seller_id"`
	LiveID   int64  `json:"live_id"`
}

// ProductSearchResponse は GET /api/admin/products/search のレスポンス。
type ProductSearchResponse struct {
	Products   []ProductSearchItem `json:"products"`
	TotalCount int64               `json:"total_count"`
}
