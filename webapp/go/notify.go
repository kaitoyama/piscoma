package main

import (
	"database/sql"
	"encoding/json"
)

type notifyExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func notifyInsert(db notifyExecer, liveID int64, userID *int64, ntype string, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		"INSERT INTO notifications (live_id, user_id, type, payload) VALUES (?, ?, ?, ?)",
		liveID, userID, ntype, body,
	)
	return err
}

func notifyBroadcast(db notifyExecer, liveID int64, ntype string, payload map[string]interface{}) error {
	return notifyInsert(db, liveID, nil, ntype, payload)
}

func notifyUser(db notifyExecer, liveID, userID int64, ntype string, payload map[string]interface{}) error {
	return notifyInsert(db, liveID, &userID, ntype, payload)
}
