package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func CredentialsPath() (string, error) {
	if override := os.Getenv("NT_CREDENTIALS_FILE"); override != "" {
		return override, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "nodelane", "tunnel", "credentials.json"), nil
}

func LoadCredentials(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var credentials Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return Credentials{}, fmt.Errorf("parse credentials: %w", err)
	}
	if credentials.ClientID == "" || credentials.ClientToken == "" {
		return Credentials{}, errors.New("credentials file is incomplete")
	}
	return credentials, nil
}

func SaveCredentials(path string, credentials Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), "credentials-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
