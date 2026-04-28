package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"unicode/utf8"

	"efb-connector/internal/auth"
)

// handleFeedbackSubmit processes feedback form submissions from the dashboard
// modal. It validates the input, enforces a daily rate limit, stores the
// feedback in the database, and optionally sends an email notification.
func (s *Server) handleFeedbackSubmit(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	category := r.FormValue("category")
	message := strings.TrimSpace(r.FormValue("message"))

	// Validate category.
	if category != "bug" && category != "feature" && category != "general" {
		category = "general"
	}

	// Validate message.
	if message == "" {
		setFlash(w, "flash.feedback_message_required")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if utf8.RuneCountInString(message) > 2000 {
		setFlash(w, "flash.feedback_message_too_long")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Rate limit: max 3 per day.
	count, err := s.db.CountFeedbackToday(userID)
	if err != nil {
		s.logger.Error("failed to count feedback", "user_id", userID, "error", err)
		setFlash(w, "flash.generic_error")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if count >= 3 {
		setFlash(w, "flash.feedback_rate_limited")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Store feedback.
	if err := s.db.CreateFeedback(userID, category, message); err != nil {
		s.logger.Error("failed to save feedback", "user_id", userID, "error", err)
		setFlash(w, "flash.generic_error")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	s.logger.Info("feedback submitted", "user_id", userID, "category", category)

	// Send email notification (best-effort).
	if s.feedbackEmail != "" {
		user, _ := s.db.GetUserByID(userID)
		userEmail := "unknown"
		if user != nil {
			userEmail = user.Email
		}
		subject := fmt.Sprintf("EFB Connector Feedback [%s]", category)
		htmlBody := fmt.Sprintf(
			`<p><strong>From:</strong> %s (user #%d)</p>
			<p><strong>Category:</strong> %s</p>
			<p><strong>Message:</strong></p>
			<p>%s</p>`,
			html.EscapeString(userEmail), userID,
			html.EscapeString(category),
			html.EscapeString(message),
		)
		if err := s.auth.SendEmail(s.feedbackEmail, subject, htmlBody); err != nil {
			s.logger.Error("failed to send feedback notification", "error", err)
		}
	}

	setFlash(w, "flash.feedback_sent")
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// handleAdminFeedback returns all feedback entries as JSON.
func (s *Server) handleAdminFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	feedback, err := s.db.GetAllFeedback(100)
	if err != nil {
		s.logger.Error("admin: get feedback", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(feedback)
}
