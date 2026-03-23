package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"

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

// Credentials for authentication resolved via 1Password, env vars, or prompts.
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

	ctx := context.Background()
	client := efb.NewEFBClient(efb.DefaultBaseURL)

	if err := client.Login(ctx, username, password); err != nil {
		log.Fatalf("Failed to login: %v", err)
	}

	fmt.Printf("Uploading GPX file: %s\n", filePath)

	if err := client.UploadFile(ctx, filePath); err != nil {
		log.Fatalf("Failed to upload GPX file: %v", err)
	}

	fmt.Println("File uploaded successfully!")
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

	// listActivity mirrors the JSON objects from the Python script list command.
	type listActivity struct {
		ID       int     `json:"id"`
		Name     string  `json:"name"`
		Type     string  `json:"type"`
		Date     string  `json:"date"`
		Duration float64 `json:"duration"`
		Distance float64 `json:"distance"`
	}

	var activities []listActivity
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

	// Get EFB credentials and create authenticated client.
	getCredentials()

	ctx := context.Background()
	efbClient := efb.NewEFBClient(efb.DefaultBaseURL)
	if err := efbClient.Login(ctx, username, password); err != nil {
		log.Fatalf("Failed to login to EFB: %v", err)
	}

	// Build Garmin credentials from environment variables.
	garminCreds := garmin.GarminCredentials{
		Email:    os.Getenv("GARMINUSERNAME"),
		Password: os.Getenv("GARMINPASSWORD"),
	}
	if garminCreds.Email == "" || garminCreds.Password == "" {
		log.Fatal("Garmin credentials required: set GARMINUSERNAME and GARMINPASSWORD environment variables")
	}

	scriptPath := getScriptPath()
	provider := garmin.NewPythonGarminProvider(scriptPath, nil)

	end := time.Now()
	start := end.AddDate(0, 0, -days)

	activities, err := provider.ListActivities(ctx, garminCreds, start, end)
	if err != nil {
		log.Fatalf("Failed to list Garmin activities: %v", err)
	}

	if len(activities) == 0 {
		fmt.Printf("No water sport activities found in the last %d days.\n", days)
		return
	}

	fmt.Printf("Found %d activities, uploading to EFB...\n", len(activities))

	successCount := 0
	for _, act := range activities {
		fmt.Printf("Uploading: %s (%s)...", act.Name, act.Date.Format("2006-01-02"))
		gpxData, err := provider.DownloadGPX(ctx, garminCreds, act.ProviderID)
		if err != nil {
			fmt.Printf(" FAILED (download): %v\n", err)
			continue
		}
		filename := fmt.Sprintf("activity_%s.gpx", act.ProviderID)
		if err := efbClient.Upload(ctx, gpxData, filename); err != nil {
			fmt.Printf(" FAILED (upload): %v\n", err)
		} else {
			fmt.Println(" OK")
			successCount++
		}
	}

	fmt.Printf("\nSync complete: %d/%d activities uploaded successfully.\n", successCount, len(activities))
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

