package okone

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// ErrTokenNotFound is returned when the token cannot be read from Chrome localStorage.
var ErrTokenNotFound = fmt.Errorf("OKOne token not found in Chrome localStorage. Please open test-okone.okg.com in Chrome and login")

// ErrTokenExpired is returned when the token exists but has expired.
var ErrTokenExpired = fmt.Errorf("OKOne token has expired. Please refresh test-okone.okg.com in Chrome to get a new token")

// ReadToken reads the OKOne Bearer token from Chrome's Local Storage LevelDB.
// It copies the LevelDB to a temp dir first to avoid Chrome's exclusive lock.
// Set NEXBOX_CHROME_DIR to override the Chrome user data directory
// (e.g. "$HOME/Library/Application Support/Google/Chrome Dev").
func ReadToken() (string, error) {
	chromeBase := os.Getenv("NEX_TOKEN_CHROME_DIR")
	if chromeBase == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		chromeBase = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	}
	srcPath := filepath.Join(chromeBase, "Default", "Local Storage", "leveldb")

	// Copy to temp dir to avoid Chrome's exclusive lock
	tmpDir, err := os.MkdirTemp("", "nexbox-chromels-")
	if err != nil {
		return "", fmt.Errorf("cannot create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "leveldb")
	if err := copyDir(srcPath, dbPath); err != nil {
		return "", fmt.Errorf("cannot copy Chrome Local Storage: %w", err)
	}

	db, err := leveldb.OpenFile(dbPath, &opt.Options{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("cannot open Chrome Local Storage copy: %w", err)
	}
	defer db.Close()

	iter := db.NewIterator(nil, nil)
	defer iter.Release()

	for iter.Next() {
		key := string(iter.Key())
		if !strings.Contains(key, "test-okone.okg.com") {
			continue
		}
		if !strings.HasSuffix(key, "\x01ok_okone") {
			continue
		}

		val := decodeValue(iter.Value())
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(val), &data); err != nil {
			return "", fmt.Errorf("failed to parse ok_okone JSON: %w", err)
		}
		token, ok := data["token"].(string)
		if !ok || token == "" {
			return "", ErrTokenNotFound
		}

		if err := validateTokenExpiry(token); err != nil {
			return "", err
		}
		return token, nil
	}

	return "", ErrTokenNotFound
}

// validateTokenExpiry checks if the JWT token has expired.
func validateTokenExpiry(token string) error {
	// Strip "Bearer " prefix
	raw := strings.TrimPrefix(token, "Bearer ")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil // not a JWT, skip validation
	}

	// Decode payload (part[1])
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil // can't decode, skip validation
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}

	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return ErrTokenExpired
	}
	return nil
}

// decodeValue decodes a Chrome localStorage value.
// Chrome prefixes values with a type byte: \x00 = UTF-16LE, \x01 = UTF-8.
func decodeValue(data []byte) string {
	if len(data) < 2 {
		return string(data)
	}
	payload := data[1:]
	if data[0] == 0x00 && len(payload)%2 == 0 {
		u16 := make([]uint16, len(payload)/2)
		for i := range u16 {
			u16[i] = binary.LittleEndian.Uint16(payload[i*2:])
		}
		return string(utf16.Decode(u16))
	}
	return string(payload)
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
		} else {
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
