package main

import (
	"bufio"
	"bytes"
	"encoding/json"
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
	fmt.Println("GPX Uploader CLI Tool")

	// Load configuration
	if err := loadConfig(); err != nil {
		log.Printf("Warning: %v", err)
	}

	// Read credentials from environment variables or prompt user
	// This function will set the username and password variables
	getCredentials()

	// Create a client with a cookie jar to maintain cookies across requests
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalf("Failed to create cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
	}

	// Prepare the POST request
	formData := url.Values{}
	formData.Set("username", username)
	formData.Set("password", password)

	req, err := http.NewRequest("POST", LoginURL, strings.NewReader(formData.Encode()))
	if err != nil {
		log.Fatalf("Failed to create POST request: %v", err)
	}

	// Add headers
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// Execute the POST request - cookies will be sent automatically by the client's jar
	postResp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to execute POST request: %v", err)
	}
	defer postResp.Body.Close()

	// Check if the login was successful
	bodyBytes, err := io.ReadAll(postResp.Body)
	if err != nil {
		log.Fatal(err)
	}
	bodyString := string(bodyBytes)
	log.Println("Response body:", bodyString)

	// Display all cookies after login
	fmt.Println("Cookies after login:")
	loginURLParsed, _ := url.Parse(LoginURL)
	for _, cookie := range jar.Cookies(loginURLParsed) {
		fmt.Printf("Cookie: %s = %s\n", cookie.Name, cookie.Value)
	}

	// Check if a file path was provided as a command-line argument
	if len(os.Args) > 1 {
		filePath := os.Args[1]
		fmt.Printf("Uploading GPX file: %s\n", filePath)

		err := uploadGPXFile(client, filePath)
		if err != nil {
			log.Fatalf("Failed to upload GPX file: %v", err)
		}

		fmt.Println("File uploaded successfully!")
	} else {
		fmt.Println("Usage: gpx-uploader [path-to-gpx-file]")
	}
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
