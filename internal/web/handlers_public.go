package web

import (
	"net/http"
	"strings"
	"time"

	"efb-connector/internal/auth"
)

// handleLanding serves the landing page. If the user is already authenticated
// it redirects to /dashboard.
func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	// Only handle the exact root path.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// If the user has a valid session, redirect to the dashboard.
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie.Value != "" {
		if _, err := s.auth.ValidateSession(cookie.Value); err == nil {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
	}

	s.render(w, "landing.html", map[string]any{
		"Flash": flash(w, r),
	})
}

// handleLoginForm renders the email input form for magic link login.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login.html", map[string]any{
		"Flash": flash(w, r),
	})
}

// handleLoginSubmit validates rate limits, generates a magic link, sends the
// email, and shows a confirmation page.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	if email == "" {
		setFlash(w, "Please enter your email address.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Rate-limit by email + IP.
	ip := remoteIP(r)
	if !s.rateLimiter.AllowLogin(email, ip) {
		s.logger.Warn("login rate limited", "email", email, "ip", ip)
		setFlash(w, "Too many login attempts. Please try again later.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Generate magic link token.
	token, err := s.auth.GenerateMagicLink(email)
	if err != nil {
		s.logger.Error("failed to generate magic link", "email", email, "error", err)
		setFlash(w, "Something went wrong. Please try again.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Send email with magic link.
	if err := s.auth.SendMagicLinkEmail(email, token, s.baseURL(r)); err != nil {
		s.logger.Error("failed to send magic link email", "email", email, "error", err)
		// Do not reveal whether the email was sent or not for security reasons.
	}

	// Always show confirmation regardless of whether the email exists or was sent.
	s.render(w, "login_sent.html", map[string]any{
		"Email": email,
	})
}

// handleVerifyMagicLink validates the magic link token, creates a session,
// sets a session cookie, and redirects to /dashboard.
func (s *Server) handleVerifyMagicLink(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		setFlash(w, "Invalid or missing login link.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	userID, err := s.auth.ValidateMagicLink(token)
	if err != nil {
		s.logger.Warn("magic link validation failed", "error", err)
		setFlash(w, "This login link is invalid or has expired. Please request a new one.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Create a new session.
	sessionToken, err := s.auth.CreateSession(userID)
	if err != nil {
		s.logger.Error("failed to create session", "user_id", userID, "error", err)
		setFlash(w, "Something went wrong. Please try again.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Set the session cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(auth.SessionMaxAge / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	s.logger.Info("user logged in", "user_id", userID)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// handleLogout destroys the current session, clears the cookie, and redirects
// to /.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err == nil && cookie.Value != "" {
		if err := s.auth.DestroySession(cookie.Value); err != nil {
			s.logger.Error("failed to destroy session", "error", err)
		}
	}

	// Clear the session cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	setFlash(w, "You have been logged out.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleImpressum renders the Impressum (legal notice) page.
func (s *Server) handleImpressum(w http.ResponseWriter, r *http.Request) {
	s.render(w, "impressum.html", nil)
}

// handlePrivacy renders the privacy policy page.
func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	s.render(w, "privacy.html", nil)
}

// baseURL returns the application base URL. It prefers the explicitly
// configured BASE_URL (from the environment) and falls back to reconstructing
// from the request's Host header and protocol headers.
func (s *Server) baseURL(r *http.Request) string {
	if s.configBaseURL != "" {
		return s.configBaseURL
	}

	scheme := "https"
	if r.TLS == nil {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

// remoteIP extracts the client IP address, respecting X-Forwarded-For when set
// (the app runs behind a reverse proxy on Fly.io).
func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first (leftmost) IP, which is the original client.
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	// Strip port from RemoteAddr (e.g. "127.0.0.1:12345").
	if idx := strings.LastIndexByte(r.RemoteAddr, ':'); idx != -1 {
		return r.RemoteAddr[:idx]
	}
	return r.RemoteAddr
}
