package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"kvm_console/model"
)

func TestLXCAccessMiddleware_OwnerAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{})
	db.AutoMigrate(&model.LXCCache{})
	db.Create(&model.LXCCache{Name: "c1", OwnerUsername: "alice"})
	model.DB = db

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "user"); c.Set("username", "alice"); c.Next() })
	r.Use(LXCAccessMiddleware())
	r.GET("/lxc/:name", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	req := httptest.NewRequest("GET", "/lxc/c1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
}

func TestLXCAccessMiddleware_ForbiddenForOthers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{})
	db.AutoMigrate(&model.LXCCache{})
	db.Create(&model.LXCCache{Name: "c1", OwnerUsername: "alice"})
	model.DB = db

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "user"); c.Set("username", "bob"); c.Next() })
	r.Use(LXCAccessMiddleware())
	r.GET("/lxc/:name", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	req := httptest.NewRequest("GET", "/lxc/c1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", w.Code)
	}
}
