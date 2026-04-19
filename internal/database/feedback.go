package database

import (
	"fmt"
	"time"
)

// Feedback mirrors the feedback table.
type Feedback struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	UserEmail string    `json:"user_email,omitempty"`
	Category  string    `json:"category"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateFeedback inserts a new feedback row.
func (d *DB) CreateFeedback(userID int64, category, message string) error {
	_, err := d.db.Exec(
		`INSERT INTO feedback (user_id, category, message) VALUES (?, ?, ?)`,
		userID, category, message,
	)
	if err != nil {
		return fmt.Errorf("database: create feedback: %w", err)
	}
	return nil
}

// CountFeedbackToday returns how many feedback entries the user submitted today (UTC).
func (d *DB) CountFeedbackToday(userID int64) (int, error) {
	var count int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM feedback WHERE user_id = ? AND created_at >= date('now')`,
		userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("database: count feedback today: %w", err)
	}
	return count, nil
}

// GetAllFeedback returns the most recent feedback entries with user emails.
func (d *DB) GetAllFeedback(limit int) ([]Feedback, error) {
	rows, err := d.db.Query(
		`SELECT f.id, f.user_id, u.email, f.category, f.message, f.created_at
		 FROM feedback f JOIN users u ON u.id = f.user_id
		 ORDER BY f.created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("database: get all feedback: %w", err)
	}
	defer rows.Close()

	var result []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt string
		if err := rows.Scan(&f.ID, &f.UserID, &f.UserEmail, &f.Category, &f.Message, &createdAt); err != nil {
			return nil, fmt.Errorf("database: scan feedback: %w", err)
		}
		f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		result = append(result, f)
	}
	return result, rows.Err()
}
