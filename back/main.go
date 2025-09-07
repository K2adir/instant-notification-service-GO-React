package main

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "strconv"
    "strings"
    "time"

    "github.com/gin-gonic/gin"
    _ "github.com/mattn/go-sqlite3"
)

// Submission represents the form data stored in SQLite
type Submission struct {
    Name    string `json:"name"`
    Email   string `json:"email"`
    Message string `json:"message"`
}

type DBSubmission struct {
    Name      string `json:"name"`
    Email     string `json:"email"`
    Message   string `json:"message"`
    Timestamp string `json:"timestamp"`
}

func main() {
    // --- Env configuration ---
    sqlitePath := getEnv("SQLITE_PATH", "./app.db")
    port := getEnv("PORT", "8080")
    allowedOriginsCSV := getEnv("ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:3000")
    allowedOrigins := parseAllowedOrigins(allowedOriginsCSV)

    // Initialize SQLite database
    db, err := sql.Open("sqlite3", sqlitePath)
    if err != nil {
        log.Fatal("Failed to open database:", err)
    }
    defer db.Close()

	// Create submissions table if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS submissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT,
			email TEXT,
			message TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}

    // Simple in-process broadcaster for SSE clients
    type broadcaster struct {
        add    chan chan string
        remove chan chan string
        send   chan string
        conns  map[chan string]struct{}
    }

    b := &broadcaster{
        add:    make(chan chan string),
        remove: make(chan chan string),
        send:   make(chan string, 64),
        conns:  make(map[chan string]struct{}),
    }

    go func() {
        for {
            select {
            case ch := <-b.add:
                b.conns[ch] = struct{}{}
            case ch := <-b.remove:
                if _, ok := b.conns[ch]; ok {
                    delete(b.conns, ch)
                    close(ch)
                }
            case msg := <-b.send:
                for ch := range b.conns {
                    select {
                    case ch <- msg:
                    default:
                        // Drop if client is slow
                    }
                }
            }
        }
    }()

    // Initialize Gin router
    r := gin.Default()
    r.RemoveExtraSlash = true

    // CORS: allow configured origins
    r.Use(func(c *gin.Context) {
        origin := c.GetHeader("Origin")
        norm := normalizeOrigin(origin)
        if allowedOrigins["*"] {
            c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
        } else if allowedOrigins[norm] {
            c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
            c.Writer.Header().Set("Vary", "Origin")
        }
        c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
        if c.Request.Method == http.MethodOptions {
            c.AbortWithStatus(http.StatusOK)
            return
        }
        c.Next()
    })

    // Root: quick service status
    r.GET("/", func(c *gin.Context) {
        c.String(http.StatusOK, "Instant Notification service is running")
    })

	// POST /api/submit-form: Handle form submission
    r.POST("/api/submit-form", func(c *gin.Context) {
        var sub Submission
        if err := c.ShouldBindJSON(&sub); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
            return
        }

		// Insert into SQLite
		_, err := db.Exec(
			"INSERT INTO submissions (name, email, message) VALUES (?, ?, ?)",
			sub.Name, sub.Email, sub.Message,
		)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save submission"})
            return
        }

        // Broadcast the new submission to SSE clients
        payload, _ := json.Marshal(sub)
        b.send <- string(payload)

        c.JSON(http.StatusOK, gin.H{"message": "Submission saved"})
    })

    // GET /api/stream/submissions: Server-Sent Events stream of new submissions
    r.GET("/api/stream/submissions", func(c *gin.Context) {
        c.Writer.Header().Set("Content-Type", "text/event-stream")
        c.Writer.Header().Set("Cache-Control", "no-cache")
        c.Writer.Header().Set("Connection", "keep-alive")
        // Mirror CORS origin if allowed
        if allowedOrigins["*"] {
            c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
        } else if origin := c.GetHeader("Origin"); allowedOrigins[normalizeOrigin(origin)] {
            c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
            c.Writer.Header().Set("Vary", "Origin")
        }

        flusher, ok := c.Writer.(http.Flusher)
        if !ok {
            c.Status(http.StatusInternalServerError)
            return
        }

        msgCh := make(chan string, 10)
        b.add <- msgCh
        defer func() { b.remove <- msgCh }()

        // Initial comment to establish stream
        fmt.Fprintf(c.Writer, ": connected\n\n")
        flusher.Flush()

        heartbeat := time.NewTicker(30 * time.Second)
        defer heartbeat.Stop()

        // Stream loop
        notify := c.Request.Context().Done()
        for {
            select {
            case <-notify:
                return
            case <-heartbeat.C:
                // Send heartbeat so connections stay alive through proxies
                fmt.Fprintf(c.Writer, ": ping %d\n\n", time.Now().Unix())
                flusher.Flush()
            case msg := <-msgCh:
                // SSE message with JSON data
                fmt.Fprintf(c.Writer, "event: submission\n")
                fmt.Fprintf(c.Writer, "data: %s\n\n", msg)
                flusher.Flush()
            }
        }
    })

    // GET /api/leads?limit=N: return recent submissions
    r.GET("/api/leads", func(c *gin.Context) {
        // Default limit
        limit := 20
        if l := c.Query("limit"); l != "" {
            if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
                limit = v
            }
        }

        rows, err := db.Query(`SELECT name, email, message, timestamp FROM submissions ORDER BY timestamp DESC LIMIT ?`, limit)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query leads"})
            return
        }
        defer rows.Close()

        var out []DBSubmission
        for rows.Next() {
            var s DBSubmission
            if err := rows.Scan(&s.Name, &s.Email, &s.Message, &s.Timestamp); err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read leads"})
                return
            }
            out = append(out, s)
        }
        if err := rows.Err(); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read leads"})
            return
        }

        c.JSON(http.StatusOK, out)
    })

    // Start server
    addr := port
    if !strings.HasPrefix(addr, ":") {
        addr = ":" + addr
    }
    log.Println("Server running on", addr)
    if err := r.Run(addr); err != nil {
        log.Fatal("Failed to start server:", err)
    }
}

// getEnv returns env var or fallback
func getEnv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

// parseAllowedOrigins parses a comma-separated list into a set
func parseAllowedOrigins(csv string) map[string]bool {
    out := make(map[string]bool)
    for _, p := range strings.Split(csv, ",") {
        v := normalizeOrigin(strings.TrimSpace(p))
        if v != "" {
            out[v] = true
        }
    }
    if len(out) == 0 {
        out["*"] = true
    }
    return out
}

// normalizeOrigin ensures origins are compared without trailing slashes
func normalizeOrigin(origin string) string {
    return strings.TrimRight(origin, "/")
}
