package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"efb-connector/internal/auth"
	"efb-connector/internal/database"
	"efb-connector/internal/i18n"
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

	s.render(w, r, "landing.html", map[string]any{
		"Flash": flash(w, r),
	})
}

// handleLoginForm renders the email input form for magic link login.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login.html", map[string]any{
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
		setFlash(w, "flash.email_required")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Reject clearly invalid email addresses (must have exactly one '@' with
	// non-empty local and domain parts, and the domain must contain a '.').
	if !isValidEmail(email) {
		setFlash(w, "flash.email_required")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Rate-limit by email + IP.
	ip := remoteIP(r)
	if !s.rateLimiter.AllowLogin(email, ip) {
		s.logger.Warn("login rate limited", "email", email, "ip", ip)
		setFlash(w, "flash.login_rate_limited")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Generate magic link token.
	token, err := s.auth.GenerateMagicLink(email)
	if err != nil {
		s.logger.Error("failed to generate magic link", "email", email, "error", err)
		setFlash(w, "flash.generic_error")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Send email with magic link.
	lang := i18n.FromContext(r.Context())
	link := s.baseURL(r) + "/auth/verify?token=" + token
	if err := s.mailer.Send(email, lang, "magic_link", map[string]any{"Link": link}); err != nil {
		s.logger.Error("failed to send magic link email", "email", email, "error", err)
		// Do not reveal whether the email was sent or not for security reasons.
	}

	// Always show confirmation regardless of whether the email exists or was sent.
	s.render(w, r, "login_sent.html", map[string]any{
		"Email": email,
	})
}

// handleVerifyMagicLinkForm renders the sign-in confirmation page.
//
// This handler is deliberately free of side effects: the token is neither
// looked up nor consumed, and no user is created. Email security scanners
// (Bitdefender, Microsoft Defender Safe Links) prefetch links found in mail,
// and Go's ServeMux serves HEAD from a GET pattern — so while verification
// happened on GET, a scanner's prefetch consumed the one-time token and the
// human's click landed on "already used" a fraction of a second later.
// Consumption now requires the POST below.
func (s *Server) handleVerifyMagicLinkForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		setFlash(w, "flash.invalid_login_link")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Format check only — no database access. This path is reachable by any
	// scanner, so it must not become an oracle for which tokens exist. It does
	// catch links a mail client truncated or line-wrapped.
	if _, err := base64.RawURLEncoding.DecodeString(token); err != nil {
		setFlash(w, "flash.invalid_login_link")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	s.render(w, r, "login_confirm.html", map[string]any{
		"Token": token,
		"Flash": flash(w, r),
	})
}

// handleVerifyMagicLinkConfirm consumes the magic link token, creates a
// session, sets the session cookie, and redirects to /dashboard.
func (s *Server) handleVerifyMagicLinkConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	if !sameOriginPost(r, s.baseURL(r)) {
		s.logger.Warn("cross-origin magic link confirm rejected",
			"ip", remoteIP(r),
			"origin", r.Header.Get("Origin"),
			"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"),
			"user_agent", r.UserAgent(),
		)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	token := r.FormValue("token")
	if token == "" {
		setFlash(w, "flash.invalid_login_link")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	userID, err := s.auth.ValidateMagicLink(token)
	if err != nil {
		s.logger.Warn("magic link validation failed",
			"error", err,
			"method", r.Method,
			"ip", remoteIP(r),
			"user_agent", r.UserAgent(),
		)
		setFlash(w, magicLinkFlashKey(err))
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Create a new session.
	sessionToken, err := s.auth.CreateSession(userID)
	if err != nil {
		s.logger.Error("failed to create session", "user_id", userID, "error", err)
		setFlash(w, "flash.generic_error")
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

	s.logger.Info("user logged in",
		"user_id", userID,
		"method", r.Method,
		"ip", remoteIP(r),
		"user_agent", r.UserAgent(),
	)

	// Show a welcome message for new users who haven't connected any services yet.
	_, _, garminErr := s.db.GetGarminCredentials(userID)
	_, _, efbErr := s.db.GetEFBCredentials(userID)
	if garminErr != nil && efbErr != nil {
		setFlash(w, "flash.welcome")
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// magicLinkFlashKey maps a validation failure to the i18n key shown to the
// user. Reporting "already used" as "expired" is what made a real bug report
// look like a clock problem, so these stay distinct.
func magicLinkFlashKey(err error) string {
	switch {
	case errors.Is(err, database.ErrMagicLinkUsed):
		return "flash.login_link_used"
	case errors.Is(err, database.ErrMagicLinkExpired):
		return "flash.login_link_expired"
	case errors.Is(err, database.ErrMagicLinkNotFound):
		return "flash.invalid_login_link"
	default:
		// A database error, or user lookup/creation failed. Not the user's
		// fault and not something a fresh link would fix.
		return "flash.generic_error"
	}
}

// sameOriginPost guards session-creating POSTs that cannot use CSRFProtect,
// which requires a session that does not exist yet at confirm time.
//
// Without it, an attacker could auto-submit their own magic link token from a
// page the victim visits, landing the victim in the attacker's account — where
// they would then store their Garmin and EFB credentials.
//
// An absent Origin means a non-browser client (curl, tests): browsers always
// send Origin on cross-origin form POSTs and scripts cannot forge it, so
// allowing the absent case does not weaken the check.
func sameOriginPost(r *http.Request, base string) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return strings.TrimSuffix(origin, "/") == strings.TrimSuffix(base, "/")
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

	setFlash(w, "flash.logged_out")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleImpressum renders the Impressum (legal notice) page.
func (s *Server) handleImpressum(w http.ResponseWriter, r *http.Request) {
	tmpl := "impressum.html"
	if i18n.FromContext(r.Context()) == i18n.EN {
		tmpl = "impressum_en.html"
	}
	s.render(w, r, tmpl, nil)
}

// handlePrivacy renders the privacy policy page.
func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	tmpl := "privacy.html"
	if i18n.FromContext(r.Context()) == i18n.EN {
		tmpl = "privacy_en.html"
	}
	s.render(w, r, tmpl, nil)
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

// isValidEmail performs a minimal sanity-check on an email address: it must
// contain exactly one '@', a non-empty local part, and a domain part that
// contains at least one '.'. Full RFC 5322 validation is intentionally omitted
// to keep the check simple and avoid false negatives with unusual but valid
// addresses.
func isValidEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	if at == -1 || at == 0 {
		// '@' not present, or local part is empty.
		return false
	}
	if strings.IndexByte(email[at+1:], '@') != -1 {
		// More than one '@'.
		return false
	}
	domain := email[at+1:]
	if len(domain) == 0 {
		return false
	}
	// Domain must contain a dot and neither part may be empty.
	dot := strings.LastIndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1
}
