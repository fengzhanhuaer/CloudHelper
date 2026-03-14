package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gin-gonic/gin"
)

// DataStore represents our JSON storage
type DataStore struct {
	mu   sync.RWMutex
	path string
	Data map[string]interface{} `json:"data"`
}

var Store *DataStore

func initStore() {
	// 确保数据目录存在
	dataDir := "./data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	dbPath := filepath.Join(dataDir, "cloudhelper.json")
	Store = &DataStore{
		path: dbPath,
		Data: make(map[string]interface{}),
	}
	
	// Load existing data if file exists
	if _, err := os.Stat(dbPath); err == nil {
		content, err := os.ReadFile(dbPath)
		if err != nil {
			log.Fatalf("failed to read JSON data file: %v", err)
		}
		if len(content) > 0 {
			if err := json.Unmarshal(content, &Store.Data); err != nil {
				log.Fatalf("failed to parse JSON data file: %v", err)
			}
		}
	} else if os.IsNotExist(err) {
		// Create an empty file
		Store.Save()
	} else {
		log.Fatalf("failed to check JSON data file: %v", err)
	}

	log.Println("JSON Datastore initialized at", dbPath)
}

func (s *DataStore) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	content, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, content, 0644)
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func main() {
	// 初始化基于JSON的存储
	initStore()

	r := gin.Default()

	// 允许跨域请求 (为了本地开发联调 Wails Client)
	r.Use(CORSMiddleware())

	// 基础健康检查接口
	r.GET("/api/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
			"service": "CloudHelper Probe Panel",
		})
	})

	log.Println("CloudHelper Probe Panel is running at http://127.0.0.1:15030")
	if err := r.Run("127.0.0.1:15030"); err != nil {
		log.Fatal(err)
	}
}
