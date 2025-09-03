package main

import (
	"database/sql"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

// Submission represents the form data stored in SQLite
type Submission struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Message string `json:"message"`
}

func main() {
	// Initialize SQLite database
	db, err := sql.Open("sqlite3", "./app.db")
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

	// Initialize Gin router
	r := gin.Default()

	// Enable CORS for React frontend (localhost:3000)
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "http://localhost:3000")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusOK)
			return
		}
		c.Next()
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

		c.JSON(http.StatusOK, gin.H{"message": "Submission saved"})
	})

	// Start server
	log.Println("Server running on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}
