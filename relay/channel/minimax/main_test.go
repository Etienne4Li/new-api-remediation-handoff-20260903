package minimax

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// Configure Gin's process-global mode once; changing it in individual tests
// races with other tests creating Gin contexts.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
