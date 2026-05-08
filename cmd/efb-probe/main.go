// Command efb-probe performs a single Login() call against the real Kanu-EFB
// portal to validate that the rate-limit detector still matches today's EFB
// output. Read EFB_USERNAME and EFB_PASSWORD from the environment, prints
// the classified error (or success) plus diagnostic markers from the raw
// response.
//
//	EFB_USERNAME=alice EFB_PASSWORD=hunter2 go run ./cmd/efb-probe
//
// Optional: EFB_BASE_URL (defaults to https://efb.kanu-efb.de).
//
// This issues exactly one HTTP POST to /login. Use sparingly while a cooldown
// is suspected — every attempt may extend EFB's per-IP cooldown window.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"efb-connector/internal/efb"
)

func main() {
	user := os.Getenv("EFB_USERNAME")
	pass := os.Getenv("EFB_PASSWORD")
	if user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "EFB_USERNAME and EFB_PASSWORD env vars are required")
		os.Exit(2)
	}
	baseURL := os.Getenv("EFB_BASE_URL")
	if baseURL == "" {
		baseURL = efb.DefaultBaseURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("== probing %s/login as %s ==\n\n", baseURL, user)

	client := efb.NewEFBClient(baseURL)
	loginErr := client.Login(ctx, user, pass)

	var rl *efb.LoginRateLimitedError
	switch {
	case loginErr == nil:
		fmt.Println("RESULT: login succeeded — EFB cooldown is NOT active for this IP/user")
	case errors.As(loginErr, &rl):
		fmt.Println("RESULT: LoginRateLimitedError — detector matched the rate-limit page")
		fmt.Printf("  status_code = %d\n", rl.StatusCode)
		fmt.Printf("  body_size   = %d bytes\n", rl.BodySize)
		fmt.Println("  body_excerpt (first 800 bytes):")
		fmt.Println(indent(snip(rl.BodyExcerpt, 800), "    "))
	default:
		fmt.Printf("RESULT: generic login error: %v\n", loginErr)
		fmt.Println("  This means: the response was the login page, but IsRateLimitedBody did NOT match.")
		fmt.Println("  Either real bad credentials, OR a rate-limit page whose markup we no longer recognise (Path C).")
		fmt.Println("  Re-fetching the login page raw to inspect markers independently…")
		diagnoseRaw(ctx, baseURL, user, pass)
	}
}

// diagnoseRaw replays the login POST without going through the EFBClient
// classifier and reports which of our two markers each appear in the body.
// Only called when the classifier returned a non-rate-limit error, so we can
// detect a Path C false negative.
func diagnoseRaw(ctx context.Context, baseURL, user, pass string) {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	form := url.Values{}
	form.Set("username", user)
	form.Set("password", pass)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/login",
		strings.NewReader(form.Encode()))
	if err != nil {
		fmt.Printf("    raw probe failed to build request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := hc.Do(req)
	if err != nil {
		fmt.Printf("    raw probe request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	prefix := make([]byte, 16*1024)
	n, _ := resp.Body.Read(prefix)
	prefix = prefix[:n]

	// The two markers IsRateLimitedBody requires.
	hasMarker1 := strings.Contains(string(prefix), "zu häufiger Login Versuche")
	hasMarker2 := strings.Contains(string(prefix), `class="alert alert-danger"`)
	finalPath := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalPath = resp.Request.URL.Path
	}

	fmt.Printf("    raw status = %d, final_path = %q, body_size_read = %d\n",
		resp.StatusCode, finalPath, n)
	fmt.Printf("    marker 'zu häufiger Login Versuche'   present: %v\n", hasMarker1)
	fmt.Printf("    marker 'class=\"alert alert-danger\"'  present: %v\n", hasMarker2)

	switch {
	case hasMarker1 && hasMarker2:
		fmt.Println("    >> BOTH markers present but classifier did NOT return LoginRateLimitedError — THIS IS A BUG.")
	case hasMarker1 && !hasMarker2:
		fmt.Println("    >> Rate-limit phrase present but the alert class marker is missing — Path C: detector too strict.")
	case !hasMarker1 && hasMarker2:
		fmt.Println("    >> Generic alert page; not a rate-limit response.")
	default:
		fmt.Println("    >> No rate-limit markers — likely genuine bad credentials.")
	}

	fmt.Println("    body excerpt (first 800 bytes):")
	fmt.Println(indent(snip(string(prefix), 800), "      "))
}

func snip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)"
}

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = pad + line
	}
	return strings.Join(lines, "\n")
}
