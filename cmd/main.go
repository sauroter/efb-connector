package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"
)

type OnePasswordConfig struct {
	Account       string `json:"account"`
	Vault         string `json:"vault"`
	Item          string `json:"item"`
	UsernameField string `json:"username_field"`
	PasswordField string `json:"password_field"`
}

type Config struct {
	OnePassword OnePasswordConfig `json:"onepassword"`
}

// GarminActivity represents an activity from Garmin Connect
type GarminActivity struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Date     string  `json:"date"`
	Duration float64 `json:"duration"`
	Distance float64 `json:"distance"`
}

// FetchResult represents the result of fetching a GPX file
type FetchResult struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Date string `json:"date"`
	File string `json:"file"`
}

var config Config

func loadConfig() error {
	configPaths := []string{
		"config.json",
		filepath.Join(os.Getenv("HOME"), ".config", "efb-connector", "config.json"),
	}

	for _, path := range configPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse config file %s: %w", path, err)
		}
		return nil
	}
	return nil
}

const (
	BaseURL   = "https://efb.kanu-efb.de/"
	LoginURL  = "https://efb.kanu-efb.de/login"
	UploadURL = "https://efb.kanu-efb.de/interpretation/usersmap"
)

// Credentials for authentication will be read from environment variables
var (
	username string
	password string
)

func main() {
	// Load configuration
	if err := loadConfig(); err != nil {
		log.Printf("Warning: %v", err)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "upload":
		if len(os.Args) < 3 {
			fmt.Println("Usage: gpx-uploader upload <path-to-gpx-file>")
			os.Exit(1)
		}
		runUpload(os.Args[2])

	case "list":
		listCmd := flag.NewFlagSet("list", flag.ExitOnError)
		days := listCmd.Int("days", 30, "Number of days to look back")
		listCmd.Parse(os.Args[2:])
		runList(*days)

	case "fetch":
		fetchCmd := flag.NewFlagSet("fetch", flag.ExitOnError)
		output := fetchCmd.String("output", ".", "Output directory")
		fetchCmd.Parse(os.Args[2:])
		if fetchCmd.NArg() < 1 {
			fmt.Println("Usage: gpx-uploader fetch <activity_id> [--output DIR]")
			os.Exit(1)
		}
		runFetch(fetchCmd.Arg(0), *output)

	case "sync":
		syncCmd := flag.NewFlagSet("sync", flag.ExitOnError)
		days := syncCmd.Int("days", 30, "Number of days to look back")
		syncCmd.Parse(os.Args[2:])
		runSync(*days)

	default:
		// Legacy behavior: treat first arg as file path for upload
		if strings.HasSuffix(command, ".gpx") {
			runUpload(command)
		} else {
			fmt.Printf("Unknown command: %s\n", command)
			printUsage()
			os.Exit(1)
		}
	}
}

func printUsage() {
	fmt.Println("GPX Uploader CLI Tool")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  gpx-uploader upload <file.gpx>       Upload a GPX file to EFB")
	fmt.Println("  gpx-uploader list [--days N]         List water sport activities from Garmin")
	fmt.Println("  gpx-uploader fetch <id> [--output D] Fetch GPX from Garmin by activity ID")
	fmt.Println("  gpx-uploader sync [--days N]         Fetch from Garmin and upload to EFB")
	fmt.Println()
	fmt.Println("Legacy:")
	fmt.Println("  gpx-uploader <file.gpx>              Upload a GPX file (same as upload)")
}

func runUpload(filePath string) {
	fmt.Println("GPX Uploader CLI Tool")

	getCredentials()

	client := createEFBClient()

	fmt.Printf("Uploading GPX file: %s\n", filePath)

	err := uploadGPXFile(client, filePath)
	if err != nil {
		log.Fatalf("Failed to upload GPX file: %v", err)
	}

	fmt.Println("File uploaded successfully!")
}

func createEFBClient() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalf("Failed to create cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
	}

	formData := url.Values{}
	formData.Set("username", username)
	formData.Set("password", password)

	req, err := http.NewRequest("POST", LoginURL, strings.NewReader(formData.Encode()))
	if err != nil {
		log.Fatalf("Failed to create POST request: %v", err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	postResp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to execute POST request: %v", err)
	}
	defer postResp.Body.Close()

	// Consume response body
	io.ReadAll(postResp.Body)

	return client
}

func getScriptPath() string {
	// Try relative to executable first
	execPath, err := os.Executable()
	if err == nil {
		scriptPath := filepath.Join(filepath.Dir(execPath), "scripts", "garmin_fetch.py")
		if _, err := os.Stat(scriptPath); err == nil {
			return scriptPath
		}
	}

	// Try relative to current working directory
	if _, err := os.Stat("scripts/garmin_fetch.py"); err == nil {
		return "scripts/garmin_fetch.py"
	}

	log.Fatal("Could not find scripts/garmin_fetch.py")
	return ""
}

func runList(days int) {
	scriptPath := getScriptPath()

	cmd := exec.Command("python", scriptPath, "list", "--days", fmt.Sprintf("%d", days), "--json")
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "%s", exitErr.Stderr)
		}
		log.Fatalf("Failed to list activities: %v", err)
	}

	var activities []GarminActivity
	if err := json.Unmarshal(output, &activities); err != nil {
		log.Fatalf("Failed to parse activities: %v", err)
	}

	if len(activities) == 0 {
		fmt.Printf("No water sport activities found in the last %d days.\n", days)
		return
	}

	fmt.Printf("Water sport activities (last %d days):\n", days)
	fmt.Println(strings.Repeat("-", 60))
	for _, act := range activities {
		durationMin := int(act.Duration / 60)
		distanceKm := act.Distance / 1000
		fmt.Printf("  %d: %s - %s\n", act.ID, act.Date, act.Name)
		fmt.Printf("           Type: %s, %d min, %.1f km\n", act.Type, durationMin, distanceKm)
	}
}

func runFetch(activityID string, outputDir string) {
	scriptPath := getScriptPath()

	cmd := exec.Command("python", scriptPath, "fetch", activityID, "--output", outputDir)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "%s", exitErr.Stderr)
		}
		log.Fatalf("Failed to fetch activity: %v", err)
	}

	filePath := strings.TrimSpace(string(output))
	fmt.Printf("Downloaded: %s\n", filePath)
}

func runSync(days int) {
	fmt.Println("Syncing water sport activities from Garmin to EFB...")

	// Get EFB credentials and create client
	getCredentials()
	efbClient := createEFBClient()

	// Fetch activities from Garmin
	scriptPath := getScriptPath()

	// Create temp directory for GPX files
	tempDir, err := os.MkdirTemp("", "gpx-sync-")
	if err != nil {
		log.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cmd := exec.Command("python", scriptPath, "fetch-all", "--days", fmt.Sprintf("%d", days), "--output", tempDir, "--json")
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "%s", exitErr.Stderr)
		}
		log.Fatalf("Failed to fetch activities: %v", err)
	}

	var results []FetchResult
	if err := json.Unmarshal(output, &results); err != nil {
		log.Fatalf("Failed to parse fetch results: %v", err)
	}

	if len(results) == 0 {
		fmt.Printf("No water sport activities found in the last %d days.\n", days)
		return
	}

	fmt.Printf("Found %d activities, uploading to EFB...\n", len(results))

	successCount := 0
	for _, result := range results {
		fmt.Printf("Uploading: %s (%s)...", result.Name, result.Date)
		err := uploadGPXFile(efbClient, result.File)
		if err != nil {
			fmt.Printf(" FAILED: %v\n", err)
		} else {
			fmt.Println(" OK")
			successCount++
		}
	}

	fmt.Printf("\nSync complete: %d/%d activities uploaded successfully.\n", successCount, len(results))
}

func getCredentials() {
	// Try 1Password first
	username, password = getCredentialsFrom1Password()
	if username != "" && password != "" {
		fmt.Println("Using credentials from 1Password")
		return
	}

	// Fall back to environment variables
	username = os.Getenv("EFBUSERNAME")
	password = os.Getenv("EFBPASSWORD")
	if username != "" && password != "" {
		return
	}

	// Fall back to interactive prompts
	reader := bufio.NewReader(os.Stdin)

	if username == "" {
		fmt.Print("Enter username: ")
		var err error
		username, err = reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Error reading username: %v", err)
		}
		username = strings.TrimSpace(username)
	}

	if password == "" {
		fmt.Print("Enter password: ")
		passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			log.Fatalf("Error reading password: %v", err)
		}
		password = string(passwordBytes)
		fmt.Println() // Add a newline after password input
	}

	if username == "" || password == "" {
		log.Fatal("Username and password must be provided")
	}
}

func getCredentialsFrom1Password() (string, string) {
	// Check if 1Password is configured
	if config.OnePassword.Account == "" || config.OnePassword.Item == "" {
		return "", ""
	}

	// Check if op CLI is available
	if _, err := exec.LookPath("op"); err != nil {
		return "", ""
	}

	// Build secret references: op://vault/item/field
	usernameRef := fmt.Sprintf("op://%s/%s/%s",
		config.OnePassword.Vault,
		config.OnePassword.Item,
		config.OnePassword.UsernameField)
	passwordRef := fmt.Sprintf("op://%s/%s/%s",
		config.OnePassword.Vault,
		config.OnePassword.Item,
		config.OnePassword.PasswordField)

	// Read username
	usernameCmd := exec.Command("op", "read", usernameRef,
		"--account", config.OnePassword.Account)
	usernameBytes, err := usernameCmd.Output()
	if err != nil {
		return "", ""
	}

	// Read password
	passwordCmd := exec.Command("op", "read", passwordRef,
		"--account", config.OnePassword.Account)
	passwordBytes, err := passwordCmd.Output()
	if err != nil {
		return "", ""
	}

	return strings.TrimSpace(string(usernameBytes)),
		strings.TrimSpace(string(passwordBytes))
}

// uploadGPXFile uploads a GPX file to the EFB portal
func uploadGPXFile(client *http.Client, filePath string) error {
	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create a new multipart writer
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create a form file field - must match the HTML input name="selectFile"
	part, err := writer.CreateFormFile("selectFile", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}

	// Copy the file content to the form field
	_, err = io.Copy(part, file)
	if err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	// Add the submit button field - required for server to process the upload
	err = writer.WriteField("uploadFile", "Datei hochladen")
	if err != nil {
		return fmt.Errorf("failed to add uploadFile field: %w", err)
	}

	// Close the multipart writer to finalize it
	err = writer.Close()
	if err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create a new request
	req, err := http.NewRequest("POST", UploadURL, body)
	if err != nil {
		return fmt.Errorf("failed to create upload request: %w", err)
	}

	// Set the content type with the boundary
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Origin", "https://efb.kanu-efb.de")
	req.Header.Set("Referer", "https://efb.kanu-efb.de/interpretation/usersmap")

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute upload request: %w", err)
	}
	defer resp.Body.Close()

	// Check the response
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Read and print the response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// activity_19151456054.gpx in Datenbank gespeichert!
	if strings.Contains(string(respBody), "Datenbank gespeichert") {
		fmt.Println("File uploaded successfully!")
		return nil
	}
	return fmt.Errorf("file upload failed: %s", string(respBody))
}
