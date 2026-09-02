package gemini

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin's mode is process-global and SetMode is not synchronized. Configure it
// once before tests so parallel subtests only perform read operations.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
