package main

// API keys created from the UI. A key is shown once, when it is created; only its SHA-256
// hash is stored, so the database never holds a usable key. (A plain hash is enough here
// because keys are long and random, not passwords.)
//
// The API requires a key as soon as one exists: either MINICRM_APIKEY is set, or at least
// one key has been created. With neither, the API is open.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

type apiKeyInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"` // first characters, to tell keys apart
	CreatedAt string `json:"createdAt"`
}

// Hashes are checked on every request, so they are cached and refreshed after each change.
var (
	keysMu    sync.RWMutex
	keyHashes [][]byte
)

func hashKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

func loadKeys(q querier) error {
	rows, err := q.Query("SELECT key_hash FROM api_keys")
	if err != nil {
		return err
	}
	defer rows.Close()
	var hashes [][]byte
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		if h, err := hex.DecodeString(s); err == nil {
			hashes = append(hashes, h)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	keysMu.Lock()
	keyHashes = hashes
	keysMu.Unlock()
	return nil
}

func storedKeyHashes() [][]byte {
	keysMu.RLock()
	defer keysMu.RUnlock()
	return keyHashes
}

// keyRequired reports whether API calls must carry a key.
func (a *App) keyRequired() bool { return a.apiKey != "" || len(storedKeyHashes()) > 0 }

func (a *App) authorised(r *http.Request) bool {
	if !a.keyRequired() {
		return true
	}
	given := r.Header.Get("MiniCRM-X-Key")
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		given = strings.TrimSpace(h[7:])
	}
	if given == "" {
		return false
	}
	ok := a.apiKey != "" && subtle.ConstantTimeCompare([]byte(given), []byte(a.apiKey)) == 1
	sum := hashKey(given)
	for _, h := range storedKeyHashes() {
		if subtle.ConstantTimeCompare(sum, h) == 1 {
			ok = true
		}
	}
	return ok
}

func listKeys(q querier) ([]apiKeyInfo, error) {
	rows, err := q.Query("SELECT id, name, prefix, created_at FROM api_keys ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []apiKeyInfo{}
	for rows.Next() {
		var k apiKeyInfo
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// createKey makes a new random key and returns it. This is the only time the key itself exists
// outside the caller's hands.
func (a *App) createKey(name string) (apiKeyInfo, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return apiKeyInfo{}, "", bad("name must be 1 to 100 characters, e.g. what the key is for")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return apiKeyInfo{}, "", err
	}
	key := "mcrm_" + hex.EncodeToString(raw)
	info := apiKeyInfo{Name: name, Prefix: key[:10], CreatedAt: nowISO()}
	err := inTx(a.db, func(tx *sql.Tx) error {
		res, err := tx.Exec("INSERT INTO api_keys(name, key_hash, prefix, created_at) VALUES(?,?,?,?)",
			info.Name, hex.EncodeToString(hashKey(key)), info.Prefix, info.CreatedAt)
		if err != nil {
			return err
		}
		info.ID, _ = res.LastInsertId()
		return logActivity(tx, 0, "", "apiKey", nil, "Created key "+info.Name)
	})
	if err != nil {
		return apiKeyInfo{}, "", err
	}
	return info, key, loadKeys(a.db)
}

func (a *App) deleteKey(id int64) error {
	err := inTx(a.db, func(tx *sql.Tx) error {
		var name string
		if err := tx.QueryRow("SELECT name FROM api_keys WHERE id = ?", id).Scan(&name); err == sql.ErrNoRows {
			return &httpError{404, fmt.Sprintf("API key %d not found", id)}
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM api_keys WHERE id = ?", id); err != nil {
			return err
		}
		return logActivity(tx, 0, "", "apiKey", "Revoked key "+name, nil)
	})
	if err != nil {
		return err
	}
	return loadKeys(a.db)
}
