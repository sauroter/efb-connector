package efb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// newMockServerSilentRejection returns a server whose upload endpoint
// answers HTTP 200 with neither the success marker nor any recognised
// hint pattern — i.e. the same shape as the production silent-rejection
// page that motivated the diagnostics work.
func newMockServerSilentRejection(t *testing.T, body string) *httptest.Server {
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
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpload_SilentRejection_ReturnsTypedError(t *testing.T) {
	body := `<html><title>eFB</title><body>Fahrtenbuch Home Meine Tracks</body></html>`
	srv := newMockServerSilentRejection(t, body)
	c := newClient(srv)

	// Set the session cookie directly so Login() doesn't need to follow
	// the (broken) redirect in the mock.
	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login: %v", err)
	}

	gpxData := []byte(`<?xml version="1.0"?><gpx></gpx>`)
	err := c.Upload(context.Background(), gpxData, "test.gpx")
	if err == nil {
		t.Fatal("expected silent-rejection error, got nil")
	}

	var rej *UploadRejectedError
	if !errorsAs(err, &rej) {
		t.Fatalf("error type = %T, want *UploadRejectedError", err)
	}
	if rej.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", rej.StatusCode)
	}
	if rej.BodySize != len(body) {
		t.Errorf("BodySize = %d, want %d", rej.BodySize, len(body))
	}
	if rej.BodyExcerpt != body {
		t.Errorf("BodyExcerpt = %q, want full body", rej.BodyExcerpt)
	}
	// Error() must keep the legacy "upload did not succeed" prefix so the
	// engine's classifyEFBError still routes this as upload_rejected.
	if !strings.Contains(rej.Error(), "upload did not succeed") {
		t.Errorf("Error() = %q, want contains %q", rej.Error(), "upload did not succeed")
	}
}

func TestUpload_SilentRejection_BodyExcerptCapped(t *testing.T) {
	// Build a body larger than MaxResponseBodyExcerpt — neither a login
	// page nor containing the success marker.
	pad := strings.Repeat("a", MaxResponseBodyExcerpt+1024)
	body := `<html><body>` + pad + `</body></html>`
	srv := newMockServerSilentRejection(t, body)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login: %v", err)
	}

	err := c.Upload(context.Background(), []byte(`<gpx></gpx>`), "test.gpx")
	var rej *UploadRejectedError
	if !errorsAs(err, &rej) {
		t.Fatalf("error type = %T, want *UploadRejectedError", err)
	}
	if rej.BodySize != len(body) {
		t.Errorf("BodySize = %d, want %d (full body size)", rej.BodySize, len(body))
	}
	if len(rej.BodyExcerpt) != MaxResponseBodyExcerpt {
		t.Errorf("len(BodyExcerpt) = %d, want %d (capped)",
			len(rej.BodyExcerpt), MaxResponseBodyExcerpt)
	}
}

func TestUploadRaw_ReturnsRawResponse(t *testing.T) {
	srv := newMockServer(t)
	c := newClient(srv)
	if err := c.Login(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("login: %v", err)
	}

	res, err := c.UploadRaw(context.Background(), []byte(`<gpx></gpx>`), "test.gpx")
	if err != nil {
		t.Fatalf("UploadRaw: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", res.StatusCode)
	}
	if !res.ContainsSuccessMarker {
		t.Error("expected ContainsSuccessMarker=true on success response")
	}
	if res.IsLoginPage {
		t.Error("expected IsLoginPage=false on success response")
	}
	if res.BodySize == 0 || len(res.Body) == 0 {
		t.Error("expected non-empty response body")
	}
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
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

// ---------------------------------------------------------------------------
// FindUnassociatedTrack tests
// ---------------------------------------------------------------------------

// tracksPageHTML returns a minimal HTML page simulating /interpretation/usersmap
// with the given track rows.
func tracksPageHTML(rows ...string) string {
	var sb strings.Builder
	sb.WriteString(`<html><body>`)
	for _, r := range rows {
		sb.WriteString(r)
	}
	sb.WriteString(`</body></html>`)
	return sb.String()
}

// trackRow returns a minimal HTML snippet for a track row.
// If hasTrip is true, the button is "edit:ID"; otherwise "track_id:ID".
func trackRow(filename, trackID string, hasTrip bool) string {
	buttonName := "track_id:" + trackID
	buttonTitle := "Fahrt neu anlegen"
	if hasTrip {
		buttonName = "edit:" + trackID
		buttonTitle = "Fahrt bearbeiten"
	}
	return `<div style="overflow:auto; padding:2px;">` +
		`<div style="float:left; width:100px;">01.01.2025</div>` +
		`<div style="float:left; width:200px;">` + filename + `</div>` +
		`<div style="float:left; width:200px;">Track Name</div>` +
		`<div style="float:left; width:100px;">` +
		`<input type="image" name="` + buttonName + `" title="` + buttonTitle + `">` +
		`</div></div>`
}

func newTracksServer(t *testing.T, html string) *httptest.Server {
	t.Helper()
	const sessionCookie = "mock-session"

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value != "1" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html)) //nolint:errcheck
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFindUnassociatedTrack_Found(t *testing.T) {
	html := tracksPageHTML(
		trackRow("garmin_123.gpx", "99", false),
		trackRow("garmin_456.gpx", "100", true),
	)
	srv := newTracksServer(t, html)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	id, err := c.FindUnassociatedTrack(context.Background(), "garmin_123.gpx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "99" {
		t.Errorf("expected track ID '99', got %q", id)
	}
}

func TestFindUnassociatedTrack_AlreadyAssociated(t *testing.T) {
	html := tracksPageHTML(
		trackRow("garmin_123.gpx", "555", true),
	)
	srv := newTracksServer(t, html)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	id, err := c.FindUnassociatedTrack(context.Background(), "garmin_123.gpx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty string for already associated track, got %q", id)
	}
}

func TestFindUnassociatedTrack_NotFound(t *testing.T) {
	html := tracksPageHTML(
		trackRow("garmin_456.gpx", "100", false),
	)
	srv := newTracksServer(t, html)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	id, err := c.FindUnassociatedTrack(context.Background(), "garmin_123.gpx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty string for not found track, got %q", id)
	}
}

// ---------------------------------------------------------------------------
// CreateTripFromTrack tests
// ---------------------------------------------------------------------------

// tripFormHTML returns a minimal trip creation form with pre-filled fields.
func tripFormHTML() string {
	return `<html><body>
<form method="POST" action="/trips/create">
<input type="hidden" name="boat_id" value="42">
<input type="hidden" name="track_id" value="99">
<input type="text" name="begdate" value="15.03.2025">
<input type="text" name="beghour" value="0">
<input type="text" name="begminute" value="0">
<input type="text" name="enddate" value="15.03.2025">
<input type="text" name="endhour" value="0">
<input type="text" name="endminute" value="0">
<select name="destination" >
  <option value="1" selected>Ziel A</option>
  <option value="2">Ziel B</option>
</select>
<select name="waters_store[]">
  <option value="10" selected>Gewaesser 1</option>
</select>
<textarea name="comment"></textarea>
<input type="submit" name="save" value="speichern">
</form>
</body></html>`
}

func newTripServer(t *testing.T, onSave func(r *http.Request)) *httptest.Server {
	t.Helper()
	const sessionCookie = "mock-session"

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})

	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Simulate click on track_id button -> serve the trip form.
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(tripFormHTML())) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/trips/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if onSave != nil {
				onSave(r)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Trip saved successfully")) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(tripFormHTML())) //nolint:errcheck
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCreateTripFromTrack_Success(t *testing.T) {
	srv := newTripServer(t, nil)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	startTime := time.Date(2025, 3, 15, 14, 30, 0, 0, time.UTC)
	err := c.CreateTripFromTrack(context.Background(), "99", startTime, 3600, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCreateTripFromTrack_TimesFilledCorrectly(t *testing.T) {
	var capturedBody string

	srv := newTripServer(t, func(r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)
		// Replace body so downstream code can still read it if needed.
		r.Body = io.NopCloser(strings.NewReader(capturedBody))
	})
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	// Start: 14:30, Duration: 5400s (1h30m) -> End: 16:00
	startTime := time.Date(2025, 3, 15, 14, 30, 0, 0, time.UTC)
	err := c.CreateTripFromTrack(context.Background(), "99", startTime, 5400, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if capturedBody == "" {
		t.Fatal("no POST body was captured")
	}

	// Verify start time fields.
	assertFormValue(t, capturedBody, "beghour", "14")
	assertFormValue(t, capturedBody, "begminute", "30")

	// Verify end time fields (14:30 + 5400s = 16:00).
	assertFormValue(t, capturedBody, "endhour", "16")
	assertFormValue(t, capturedBody, "endminute", "0")
	assertFormValue(t, capturedBody, "enddate", "15.03.2025")

	// Verify the save button was included.
	assertFormValue(t, capturedBody, "save", "speichern")

	// Verify pre-filled fields were preserved.
	assertFormValue(t, capturedBody, "boat_id", "42")
}

// assertFormValue checks that the URL-encoded body contains name=expectedValue.
func assertFormValue(t *testing.T, body, name, expectedValue string) {
	t.Helper()
	vals, err := url.ParseQuery(body)
	if err != nil {
		t.Fatalf("failed to parse form body: %v", err)
	}
	got := vals.Get(name)
	if got != expectedValue {
		t.Errorf("form field %q: expected %q, got %q", name, expectedValue, got)
	}
}

func TestCreateTripFromTrack_SubmitFailure(t *testing.T) {
	const sessionCookie = "mock-session"

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		// Serve the form on POST (simulating the track_id click).
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(tripFormHTML())) //nolint:errcheck
	})
	mux.HandleFunc("/trips/create", func(w http.ResponseWriter, r *http.Request) {
		// Return 500 on the save POST.
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	startTime := time.Date(2025, 3, 15, 14, 30, 0, 0, time.UTC)
	err := c.CreateTripFromTrack(context.Background(), "99", startTime, 3600, nil)
	if err == nil {
		t.Fatal("expected error for server 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// C1: selectedOptionRe handles value before selected
// ---------------------------------------------------------------------------

func TestParseFormFields_SelectValueBeforeSelected(t *testing.T) {
	html := `<form>
<select name="boat">
  <option value="1">Boat A</option>
  <option value="2" selected>Boat B</option>
  <option value="3">Boat C</option>
</select>
</form>`

	vals := parseFormFields(html)
	got := vals.Get("boat")
	if got != "2" {
		t.Errorf("expected selected value '2' when value precedes selected, got %q", got)
	}
}

func TestParseFormFields_SelectSelectedBeforeValue(t *testing.T) {
	// Also verify the original order still works.
	html := `<form>
<select name="boat">
  <option value="1">Boat A</option>
  <option selected value="2">Boat B</option>
</select>
</form>`

	vals := parseFormFields(html)
	got := vals.Get("boat")
	if got != "2" {
		t.Errorf("expected selected value '2' when selected precedes value, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// C2: parseUnassociatedTrack respects row boundaries
// ---------------------------------------------------------------------------

func TestParseUnassociatedTrack_NoBleedBetweenRows(t *testing.T) {
	// Row 1: garmin_AAA.gpx with track_id:50 (unassociated)
	// Row 2: garmin_BBB.gpx with edit:60 (already associated)
	//
	// When searching for garmin_BBB.gpx, we should NOT pick up
	// track_id:50 from row 1.
	html := tracksPageHTML(
		trackRow("garmin_AAA.gpx", "50", false),
		trackRow("garmin_BBB.gpx", "60", true),
	)

	id := parseUnassociatedTrack(html, "garmin_BBB.gpx")
	if id != "" {
		t.Errorf("expected empty string for already associated track, got %q (bleed from adjacent row)", id)
	}
}

func TestParseUnassociatedTrack_CorrectRowSelected(t *testing.T) {
	// Three adjacent rows; only the middle one matches our filename and is
	// unassociated. The other rows have different states.
	html := tracksPageHTML(
		trackRow("garmin_111.gpx", "10", true),  // associated
		trackRow("garmin_222.gpx", "20", false), // unassociated — target
		trackRow("garmin_333.gpx", "30", true),  // associated
	)

	id := parseUnassociatedTrack(html, "garmin_222.gpx")
	if id != "20" {
		t.Errorf("expected track ID '20', got %q", id)
	}
}

func TestParseUnassociatedTrack_FilenameInWrongRowIgnored(t *testing.T) {
	// The filename appears in a row that has edit (associated), while an
	// adjacent row has track_id (unassociated) for a DIFFERENT file.
	// We must not return the wrong track ID.
	html := tracksPageHTML(
		trackRow("garmin_TARGET.gpx", "77", true),   // associated — our file
		trackRow("garmin_OTHER.gpx", "88", false),    // unassociated — different file
	)

	id := parseUnassociatedTrack(html, "garmin_TARGET.gpx")
	if id != "" {
		t.Errorf("expected empty string for associated track, got %q", id)
	}
}

// ---------------------------------------------------------------------------
// I1: Error indicator detection in trip save response
// ---------------------------------------------------------------------------

func TestCreateTripFromTrack_ErrorIndicatorInResponse(t *testing.T) {
	const sessionCookie = "mock-session"

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(tripFormHTML())) //nolint:errcheck
	})
	mux.HandleFunc("/trips/create", func(w http.ResponseWriter, r *http.Request) {
		// Return 200 but with an error indicator in the body.
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Fehler: Datum ungültig")) //nolint:errcheck
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	startTime := time.Date(2025, 3, 15, 14, 30, 0, 0, time.UTC)
	err := c.CreateTripFromTrack(context.Background(), "99", startTime, 3600, nil)
	if err == nil {
		t.Fatal("expected error when response contains 'Fehler', got nil")
	}
	if !strings.Contains(err.Error(), "error indicator") {
		t.Errorf("expected 'error indicator' in error message, got: %v", err)
	}
}

func TestCreateTripFromTrack_WithEnrichment(t *testing.T) {
	var capturedBody string

	srv := newTripServer(t, func(r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)
		r.Body = io.NopCloser(strings.NewReader(capturedBody))
	})
	c := newClient(srv)

	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	enrichment := &TripEnrichment{
		Sections: []SectionEnrichment{
			{
				SectionName:  "Saalach [Lofer - Scheffsnoth]",
				Grade:        "III-IV",
				SpotGrades:   []string{"V", "VI"},
				GaugeName:    "Lofer",
				GaugeReading: "47 cm",
				GaugeFlow:    "12.3 m\u00b3/s",
				WaterLevel:   "Medium water",
			},
		},
	}

	startTime := time.Date(2025, 3, 15, 14, 30, 0, 0, time.UTC)
	err := c.CreateTripFromTrack(context.Background(), "99", startTime, 3600, enrichment)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if capturedBody == "" {
		t.Fatal("no POST body was captured")
	}

	// Parse the submitted form to extract the comment field.
	vals, err := url.ParseQuery(capturedBody)
	if err != nil {
		t.Fatalf("failed to parse form body: %v", err)
	}
	comment := vals.Get("comment")

	// The comment should contain the enrichment block.
	if !strings.Contains(comment, "---") {
		t.Error("comment should contain enrichment separator '---'")
	}
	if !strings.Contains(comment, "Rivermap:") {
		t.Error("comment should contain 'Rivermap:' header")
	}
	if !strings.Contains(comment, "Saalach [Lofer - Scheffsnoth] (III-IV)") {
		t.Error("comment should contain section info")
	}
	if !strings.Contains(comment, "Lofer:") {
		t.Error("comment should contain gauge name")
	}
	if !strings.Contains(comment, "12.3 m\u00b3/s") {
		t.Error("comment should contain gauge flow")
	}
	if !strings.Contains(comment, "Medium water") {
		t.Error("comment should contain water level")
	}
	if !strings.Contains(comment, "Data: rivermap.org (CC BY-SA 4.0)") {
		t.Error("comment should contain attribution")
	}
}

// ---------------------------------------------------------------------------
// I2: HTML entity unescaping in parseFormFields
// ---------------------------------------------------------------------------

func TestSummariseResponse(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantSubs []string // substrings that must appear in the result
	}{
		{
			name:     "title only",
			body:     `<html><title>eFB - elektronisches Fahrtenbuch</title><body>ok</body></html>`,
			wantSubs: []string{`page title: "eFB - elektronisches Fahrtenbuch"`, "bytes)", `body excerpt: "ok"`},
		},
		{
			name: "title plus body excerpt when no hint or alert",
			body: `<html><title>eFB</title><body>` +
				`<script>var x = 1;</script>` +
				`<p>Bitte wählen Sie ein Gewässer aus der Liste</p>` +
				`<style>p { color: red }</style>` +
				`</body></html>`,
			wantSubs: []string{
				`page title: "eFB"`,
				`body excerpt: "Bitte wählen Sie ein Gewässer aus der Liste"`,
			},
		},
		{
			name: "alert suppresses body excerpt",
			body: `<html><title>eFB</title>` +
				`<div class="alert">Upload fehlgeschlagen</div>` +
				`<body><p>Other text</p></body></html>`,
			wantSubs: []string{`alert: "Upload fehlgeschlagen"`},
		},
		{
			name:     "title plus hint",
			body:     `<html><title>eFB</title><body>Datei existiert bereits im System</body></html>`,
			wantSubs: []string{`page title: "eFB"`, "hint: track already exists"},
		},
		{
			name:     "title plus alert div",
			body:     `<html><title>eFB</title><div class="alert alert-danger">Upload fehlgeschlagen</div></html>`,
			wantSubs: []string{`page title: "eFB"`, `alert: "Upload fehlgeschlagen"`},
		},
		{
			name:     "hint without title",
			body:     `Keine GPS-Daten in der Datei gefunden`,
			wantSubs: []string{"hint: no GPS data in file", "bytes)"},
		},
		{
			name:     "no title no hints short body",
			body:     `just some text`,
			wantSubs: []string{"just some text"},
		},
		{
			name:     "no title no hints long body truncated",
			body:     string(make([]byte, 300)),
			wantSubs: []string{"…"},
		},
		{
			name:     "alert div with nested HTML stripped",
			body:     `<div class="error"><b>Fehler:</b> Datei <em>ungültig</em></div>`,
			wantSubs: []string{`alert:`, "Fehler:", "ungültig", "hint: invalid file"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summariseResponse([]byte(tc.body))
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("summariseResponse() = %q, want substring %q", got, sub)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EFB v2026.1 track-usage consent gate
// ---------------------------------------------------------------------------

// consentGateBody is a minimal stand-in for the real EFB consent page.
// It must contain both the German marker phrase and the commit_tracks
// button name to be recognised.
const consentGateBody = `<html><head><title>eFB</title></head><body>
<p>Das Hochladen kann erst durchgeführt werden, wenn Ihr
   der anonymisierten Verwendung Eurer Tracks zugestimmt habt.</p>
<form method="post"><input type="submit" name="commit_tracks" value="ich stimme zu"/></form>
</body></html>`

func TestIsConsentRequiredBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"matches consent page", consentGateBody, true},
		{"phrase only, no button", `nur die anonymisierten Verwendung Eurer Tracks zugestimmt - keine form`, false},
		{"button only, no phrase", `<input name="commit_tracks">`, false},
		{"empty body", ``, false},
		{"normal upload page", `<form><input type="file" name="selectFile"/></form>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsConsentRequiredBody([]byte(tc.body))
			if got != tc.want {
				t.Errorf("IsConsentRequiredBody(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestSummariseResponse_ConsentHint(t *testing.T) {
	got := summariseResponse([]byte(consentGateBody))
	if !strings.Contains(got, "EFB consent required") {
		t.Errorf("summary missing consent hint, got: %q", got)
	}
}

// newMockServerConsentGate authenticates any user and answers GET on
// /interpretation/usersmap with the consent-gate page (no upload form).
func newMockServerConsentGate(t *testing.T) *httptest.Server {
	t.Helper()
	const sessionCookie = "mock-session"

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(consentGateBody))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckConsentGate_Required(t *testing.T) {
	srv := newMockServerConsentGate(t)
	c := newClient(srv)
	if err := c.Login(context.Background(), "any", "any"); err != nil {
		t.Fatalf("login: %v", err)
	}
	required, err := c.CheckConsentGate(context.Background())
	if err != nil {
		t.Fatalf("CheckConsentGate: %v", err)
	}
	if !required {
		t.Error("expected consent_required=true on consent-gate page")
	}
}

func TestCheckConsentGate_NotRequired(t *testing.T) {
	// newMockServer returns "<html>usersmap</html>" on GET — no consent
	// markers, so the helper should report not-required.
	srv := newMockServer(t)
	c := newClient(srv)
	if err := c.Login(context.Background(), "valid", "correct"); err != nil {
		t.Fatalf("login: %v", err)
	}
	required, err := c.CheckConsentGate(context.Background())
	if err != nil {
		t.Fatalf("CheckConsentGate: %v", err)
	}
	if required {
		t.Error("expected consent_required=false on normal tracks page")
	}
}

func TestParseFormFields_HTMLEntityUnescaping(t *testing.T) {
	html := `<form>
<input type="hidden" name="location" value="M&uuml;nchen &amp; Berlin">
<select name="water">
  <option value="Gew&auml;sser &amp; See" selected>Water</option>
</select>
<textarea name="comment">Stra&szlig;e &lt;1&gt;</textarea>
</form>`

	vals := parseFormFields(html)

	tests := []struct {
		field    string
		expected string
	}{
		{"location", "München & Berlin"},
		{"water", "Gewässer & See"},
		{"comment", "Straße <1>"},
	}
	for _, tc := range tests {
		got := vals.Get(tc.field)
		if got != tc.expected {
			t.Errorf("field %q: expected %q, got %q", tc.field, tc.expected, got)
		}
	}
}
