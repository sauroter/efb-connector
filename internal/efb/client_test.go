package efb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newMockServer creates a test HTTP server that simulates the EFB portal.
//
// Routes:
//   - POST /login            — accepts user "valid" / pass "correct";
//     anything else redirects back to /login
//   - POST /interpretation/usersmap — requires the mock session cookie;
//     returns "Datenbank gespeichert" on success, 403 otherwise
//   - GET  /interpretation/usersmap — requires the mock session cookie;
//     redirects to /login otherwise
func newMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	const sessionCookie = "mock-session"

	mux := http.NewServeMux()

	// Login endpoint
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		user := r.FormValue("username")
		pass := r.FormValue("password")
		if user == "valid" && pass == "correct" {
			// Set a session cookie and redirect to the main page.
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		// Bad credentials — redirect back to /login (EFB standard behaviour).
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	// Upload endpoint
	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		// Check for valid session cookie.
		cookie, err := r.Cookie(sessionCookie)
		hasSession := err == nil && cookie.Value == "1"

		switch r.Method {
		case http.MethodGet:
			if !hasSession {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html>usersmap</html>")) //nolint:errcheck

		case http.MethodPost:
			if !hasSession {
				http.Error(w, "not authenticated", http.StatusForbidden)
				return
			}
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				http.Error(w, "bad multipart", http.StatusBadRequest)
				return
			}
			_, _, err := r.FormFile("selectFile")
			if err != nil {
				http.Error(w, "missing selectFile", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("activity_test.gpx in Datenbank gespeichert!")) //nolint:errcheck

		default:
			http.NotFound(w, r)
		}
	})

	// Root — landing page after successful login.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html>home</html>")) //nolint:errcheck
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newClient builds an EFBClient pointing at the mock server.
func newClient(srv *httptest.Server) *EFBClient {
	return NewEFBClient(srv.URL)
}

// ---------------------------------------------------------------------------
// Login tests
// ---------------------------------------------------------------------------

func TestLogin_Success(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	if err := c.Login(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestLogin_Failure_WrongCredentials(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	err := c.Login(context.Background(), "wrong", "credentials")
	if err == nil {
		t.Fatal("expected error for wrong credentials, got nil")
	}
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Errorf("expected 'invalid credentials' in error, got: %v", err)
	}
}

func TestLogin_ContextCancelled(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := c.Login(ctx, "valid", "correct")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// Upload tests
// ---------------------------------------------------------------------------

func TestUpload_Success(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	// Login first to get a session cookie.
	if err := c.Login(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	gpxData := []byte(`<?xml version="1.0"?><gpx></gpx>`)
	if err := c.Upload(context.Background(), gpxData, "test.gpx"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestUpload_NotAuthenticated(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)
	// Do NOT call Login — upload should fail.

	gpxData := []byte(`<?xml version="1.0"?><gpx></gpx>`)
	err := c.Upload(context.Background(), gpxData, "test.gpx")
	if err == nil {
		t.Fatal("expected error for unauthenticated upload, got nil")
	}
}

// mockServerNoSuccessMarker returns a server whose upload endpoint never
// returns "Datenbank gespeichert".
func newMockServerNoSuccessMarker(t *testing.T) *httptest.Server {
	t.Helper()

	const sessionCookie = "mock-session"

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})

	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			// Deliberately missing the success marker.
			w.Write([]byte("Fehler beim Hochladen")) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpload_MissingSuccessMarker(t *testing.T) {
	srv := newMockServerNoSuccessMarker(t)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	gpxData := []byte(`<?xml version="1.0"?><gpx></gpx>`)
	err := c.Upload(context.Background(), gpxData, "test.gpx")
	if err == nil {
		t.Fatal("expected error when success marker is absent, got nil")
	}
}

// ---------------------------------------------------------------------------
// UploadFile tests
// ---------------------------------------------------------------------------

func TestUploadFile_Success(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	if err := c.Login(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	// Write a temporary GPX file.
	tmpDir := t.TempDir()
	gpxPath := filepath.Join(tmpDir, "test.gpx")
	if err := os.WriteFile(gpxPath, []byte(`<?xml version="1.0"?><gpx></gpx>`), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	if err := c.UploadFile(context.Background(), gpxPath); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestUploadFile_FileNotFound(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	if err := c.Login(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	err := c.UploadFile(context.Background(), "/nonexistent/path/file.gpx")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// ---------------------------------------------------------------------------
// IsSessionValid tests
// ---------------------------------------------------------------------------

func TestIsSessionValid_Valid(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	if err := c.Login(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	if !c.IsSessionValid(context.Background()) {
		t.Fatal("expected session to be valid after login")
	}
}

func TestIsSessionValid_Expired(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)
	// Do NOT login — cookie jar is empty, so the server redirects to /login.

	if c.IsSessionValid(context.Background()) {
		t.Fatal("expected session to be invalid without login")
	}
}

// ---------------------------------------------------------------------------
// ValidateCredentials tests
// ---------------------------------------------------------------------------

func TestValidateCredentials_Success(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	if err := c.ValidateCredentials(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateCredentials_WrongPassword(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)

	err := c.ValidateCredentials(context.Background(), "valid", "wrong")
	if err == nil {
		t.Fatal("expected error for wrong credentials, got nil")
	}
}
