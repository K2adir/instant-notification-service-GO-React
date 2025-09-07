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
    // Optional client-side submit timestamp in milliseconds since epoch
    ClientSubmitAt int64 `json:"clientSubmitAt,omitempty"`
}

type DBSubmission struct {
    ID        int64  `json:"id"`
    Name      string `json:"name"`
    Email     string `json:"email"`
    Message   string `json:"message"`
    Timestamp string `json:"timestamp"`
    ClientSubmitAtMs        sql.NullInt64 `json:"clientSubmitAtMs"`
    ServerBroadcastAtMs     sql.NullInt64 `json:"serverBroadcastAtMs"`
    ClientSubmitToServerMs  sql.NullInt64 `json:"clientSubmitToServerMs"`
    ClientServerToDisplayMs sql.NullInt64 `json:"clientServerToDisplayMs"`
    ClientSubmitToDisplayMs sql.NullInt64 `json:"clientSubmitToDisplayMs"`
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
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			client_submit_at_ms INTEGER,
			server_broadcast_at_ms INTEGER,
			client_submit_to_server_ms INTEGER,
			client_server_to_display_ms INTEGER,
			client_submit_to_display_ms INTEGER
		)
	`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}

	// Best-effort migrations for existing DBs; ignore errors if columns already exist
	_, _ = db.Exec(`ALTER TABLE submissions ADD COLUMN client_submit_at_ms INTEGER`)
	_, _ = db.Exec(`ALTER TABLE submissions ADD COLUMN server_broadcast_at_ms INTEGER`)
	_, _ = db.Exec(`ALTER TABLE submissions ADD COLUMN client_submit_to_server_ms INTEGER`)
	_, _ = db.Exec(`ALTER TABLE submissions ADD COLUMN client_server_to_display_ms INTEGER`)
	_, _ = db.Exec(`ALTER TABLE submissions ADD COLUMN client_submit_to_display_ms INTEGER`)

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
		res, err := db.Exec(
			"INSERT INTO submissions (name, email, message, client_submit_at_ms) VALUES (?, ?, ?, ?)",
			sub.Name, sub.Email, sub.Message, sub.ClientSubmitAt,
		)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save submission"})
            return
        }

        id, _ := res.LastInsertId()
        serverBroadcastAt := time.Now().UnixMilli()
        _, _ = db.Exec("UPDATE submissions SET server_broadcast_at_ms = ? WHERE id = ?", serverBroadcastAt, id)

        // Broadcast the new submission to SSE clients with ids and timestamps
        payload, _ := json.Marshal(struct {
            ID              int64  `json:"id"`
            Name            string `json:"name"`
            Email           string `json:"email"`
            Message         string `json:"message"`
            ClientSubmitAt  int64  `json:"clientSubmitAt,omitempty"`
            ServerBroadcast int64  `json:"serverBroadcastAt"`
        }{
            ID:              id,
            Name:            sub.Name,
            Email:           sub.Email,
            Message:         sub.Message,
            ClientSubmitAt:  sub.ClientSubmitAt,
            ServerBroadcast: serverBroadcastAt,
        })
        b.send <- string(payload)

        c.JSON(http.StatusOK, gin.H{"message": "Submission saved", "id": id})
    })

    // GET /api/submit-form: Return recent submissions (proof endpoint)
    r.GET("/api/submit-form", func(c *gin.Context) {
        // Default to last 10 for quick proof
        limit := 10
        if l := c.Query("limit"); l != "" {
            if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
                limit = v
            }
        }

        rows, err := db.Query(`SELECT id, name, email, message, timestamp,
            client_submit_at_ms, server_broadcast_at_ms,
            client_submit_to_server_ms, client_server_to_display_ms, client_submit_to_display_ms
            FROM submissions ORDER BY timestamp DESC LIMIT ?`, limit)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query submissions"})
            return
        }
        defer rows.Close()

        type apiRow struct {
            ID        int64   `json:"id"`
            Name      string  `json:"name"`
            Email     string  `json:"email"`
            Message   string  `json:"message"`
            Timestamp string  `json:"timestamp"`
            ClientSubmitAtMs        *int64 `json:"clientSubmitAtMs,omitempty"`
            ServerBroadcastAtMs     *int64 `json:"serverBroadcastAtMs,omitempty"`
            ClientSubmitToServerMs  *int64 `json:"clientSubmitToServerMs,omitempty"`
            ClientServerToDisplayMs *int64 `json:"clientServerToDisplayMs,omitempty"`
            ClientSubmitToDisplayMs *int64 `json:"clientSubmitToDisplayMs,omitempty"`
        }
        ptr := func(n sql.NullInt64) *int64 { if n.Valid { v := n.Int64; return &v }; return nil }
        var out []apiRow
        for rows.Next() {
            var s DBSubmission
            if err := rows.Scan(&s.ID, &s.Name, &s.Email, &s.Message, &s.Timestamp, &s.ClientSubmitAtMs, &s.ServerBroadcastAtMs, &s.ClientSubmitToServerMs, &s.ClientServerToDisplayMs, &s.ClientSubmitToDisplayMs); err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read submissions"})
                return
            }
            out = append(out, apiRow{
                ID:        s.ID,
                Name:      s.Name,
                Email:     s.Email,
                Message:   s.Message,
                Timestamp: s.Timestamp,
                ClientSubmitAtMs:        ptr(s.ClientSubmitAtMs),
                ServerBroadcastAtMs:     ptr(s.ServerBroadcastAtMs),
                ClientSubmitToServerMs:  ptr(s.ClientSubmitToServerMs),
                ClientServerToDisplayMs: ptr(s.ClientServerToDisplayMs),
                ClientSubmitToDisplayMs: ptr(s.ClientSubmitToDisplayMs),
            })
        }
        if err := rows.Err(); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read submissions"})
            return
        }

        c.JSON(http.StatusOK, out)
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

        rows, err := db.Query(`SELECT id, name, email, message, timestamp,
            client_submit_at_ms, server_broadcast_at_ms,
            client_submit_to_server_ms, client_server_to_display_ms, client_submit_to_display_ms
            FROM submissions ORDER BY timestamp DESC LIMIT ?`, limit)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query leads"})
            return
        }
        defer rows.Close()

        type apiRow2 struct {
            ID        int64   `json:"id"`
            Name      string  `json:"name"`
            Email     string  `json:"email"`
            Message   string  `json:"message"`
            Timestamp string  `json:"timestamp"`
            ClientSubmitAtMs        *int64 `json:"clientSubmitAtMs,omitempty"`
            ServerBroadcastAtMs     *int64 `json:"serverBroadcastAtMs,omitempty"`
            ClientSubmitToServerMs  *int64 `json:"clientSubmitToServerMs,omitempty"`
            ClientServerToDisplayMs *int64 `json:"clientServerToDisplayMs,omitempty"`
            ClientSubmitToDisplayMs *int64 `json:"clientSubmitToDisplayMs,omitempty"`
        }
        ptr2 := func(n sql.NullInt64) *int64 { if n.Valid { v := n.Int64; return &v }; return nil }
        var out []apiRow2
        for rows.Next() {
            var s DBSubmission
            if err := rows.Scan(&s.ID, &s.Name, &s.Email, &s.Message, &s.Timestamp, &s.ClientSubmitAtMs, &s.ServerBroadcastAtMs, &s.ClientSubmitToServerMs, &s.ClientServerToDisplayMs, &s.ClientSubmitToDisplayMs); err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read leads"})
                return
            }
            out = append(out, apiRow2{
                ID:        s.ID,
                Name:      s.Name,
                Email:     s.Email,
                Message:   s.Message,
                Timestamp: s.Timestamp,
                ClientSubmitAtMs:        ptr2(s.ClientSubmitAtMs),
                ServerBroadcastAtMs:     ptr2(s.ServerBroadcastAtMs),
                ClientSubmitToServerMs:  ptr2(s.ClientSubmitToServerMs),
                ClientServerToDisplayMs: ptr2(s.ClientServerToDisplayMs),
                ClientSubmitToDisplayMs: ptr2(s.ClientSubmitToDisplayMs),
            })
        }
        if err := rows.Err(); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read leads"})
            return
        }

        c.JSON(http.StatusOK, out)
    })

    // POST /api/submissions/:id/latency: store client-reported latency numbers
    r.POST("/api/submissions/:id/latency", func(c *gin.Context) {
        idStr := c.Param("id")
        id, err := strconv.ParseInt(idStr, 10, 64)
        if err != nil || id <= 0 {
            c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
            return
        }
        var body struct {
            SubmitToServerMs  *int64 `json:"submitToServerMs"`
            ServerToDisplayMs *int64 `json:"serverToDisplayMs"`
            SubmitToDisplayMs *int64 `json:"submitToDisplayMs"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
            return
        }
        sets := []string{}
        args := []interface{}{}
        if body.SubmitToServerMs != nil {
            sets = append(sets, "client_submit_to_server_ms = ?")
            args = append(args, *body.SubmitToServerMs)
        }
        if body.ServerToDisplayMs != nil {
            sets = append(sets, "client_server_to_display_ms = ?")
            args = append(args, *body.ServerToDisplayMs)
        }
        if body.SubmitToDisplayMs != nil {
            sets = append(sets, "client_submit_to_display_ms = ?")
            args = append(args, *body.SubmitToDisplayMs)
        }
        if len(sets) == 0 {
            c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
            return
        }
        args = append(args, id)
        q := "UPDATE submissions SET " + strings.Join(sets, ", ") + " WHERE id = ?"
        if _, err := db.Exec(q, args...); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update latency"})
            return
        }
        c.JSON(http.StatusOK, gin.H{"ok": true})
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
