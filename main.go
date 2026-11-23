// MiniCRM — web app + REST API in a single binary.
//
//	minicrm                                  start the server
//	minicrm version                          print the version and exit
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed public
var embedded embed.FS

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultDBPath returns the path to the SQLite database.
// Priority order:
//  1. ./data/crm.db  — present in the working directory (dev / legacy installs)
//  2. ~/Library/Application Support/minicrm/crm.db  — standard macOS location
//  3. ./data/crm.db  — fallback when neither exists yet
func defaultDBPath() string {
	local := filepath.Join("data", "crm.db")
	if _, err := os.Stat(local); err == nil {
		return local
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Library", "Application Support", "minicrm", "crm.db")
	}
	return local
}

func main() {
	version = strings.TrimPrefix(version, "v") // "v1.2.0" and "1.2.0" both mean 1.2.0
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	log.SetFlags(log.LstdFlags)
	dbPath := env("DB_PATH", defaultDBPath())
	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("opening database %s: %v", dbPath, err)
	}
	defer db.Close()
	app := &App{db: db, apiKey: env("MINICRM_APIKEY", os.Getenv("API_KEY"))} // API_KEY: older name, still honoured
	if err := loadKeys(db); err != nil {
		log.Fatalf("loading API keys: %v", err)
	}
	if err := loadBuckets(db); err != nil {
		log.Fatalf("loading buckets: %v", err)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "keys-reset": // the way back in when every key has been lost
			res, err := db.Exec("DELETE FROM api_keys")
			if err != nil {
				db.Close()
				log.Fatal(err)
			}
			n, _ := res.RowsAffected()
			fmt.Printf("Removed %d API key(s). Restart the server; the API is open again unless MINICRM_APIKEY is set.\n", n)
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown command %q. Run with no arguments to start the server, or: keys-reset\n", os.Args[1])
			os.Exit(2)
		}
	}

	// Static files: embedded in the binary, or served from disk when PUBLIC_DIR is set (for UI work).
	var static http.FileSystem
	if dir := os.Getenv("PUBLIC_DIR"); dir != "" {
		static = http.Dir(dir)
	} else {
		sub, err := fs.Sub(embedded, "public")
		if err != nil {
			log.Fatal(err)
		}
		static = http.FS(sub)
	}
	files := http.FileServer(static)

	mux := http.NewServeMux()
	mux.Handle("/api", app)
	mux.Handle("/api/", app)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})

	addr := env("HOST", "127.0.0.1") + ":" + env("PORT", "4300")
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	auth := ""
	if app.apiKey != "" {
		auth = ", API key required"
	}
	log.Printf("MiniCRM %s running at http://%s  (API under /api%s, database %s)", version, addr, auth, dbPath)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
