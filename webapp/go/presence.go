package main

import "github.com/jmoiron/sqlx"

const viewerWindowSeconds = 30

func touchPresence(db *sqlx.DB, liveID, userID int64) error {
	_, err := db.Exec(
		`INSERT INTO live_viewers (live_id, user_id, last_seen_at)
		 VALUES (?, ?, NOW(3))
		 ON DUPLICATE KEY UPDATE last_seen_at = NOW(3)`,
		liveID, userID,
	)
	return err
}

func viewerCount(db *sqlx.DB, liveID int64) (int64, error) {
	var count int64
	err := db.Get(
		&count,
		`SELECT COUNT(*) FROM live_viewers
		 WHERE live_id = ? AND last_seen_at > NOW(3) - INTERVAL ? SECOND`,
		liveID, viewerWindowSeconds,
	)
	return count, err
}

func reactionCount(db *sqlx.DB, liveID int64) (int64, error) {
	var count int64
	err := db.Get(&count, "SELECT COUNT(*) FROM reactions WHERE live_id = ?", liveID)
	return count, err
}
