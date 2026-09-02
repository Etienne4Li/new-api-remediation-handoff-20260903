package channel

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin stores its mode in package globals without synchronization. Set it once
// before the package's tests start; per-test SetMode calls race with parallel
// tests that construct contexts and read the same globals.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
