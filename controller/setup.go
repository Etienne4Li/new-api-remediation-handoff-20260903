package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type Setup struct {
	Status       bool   `json:"status"`
	RootInit     bool   `json:"root_init"`
	DatabaseType string `json:"database_type"`
}

type SetupRequest struct {
	Username           string `json:"username"`
	Password           string `json:"password"`
	ConfirmPassword    string `json:"confirmPassword"`
	SelfUseModeEnabled bool   `json:"SelfUseModeEnabled"`
	DemoSiteEnabled    bool   `json:"DemoSiteEnabled"`
}

func GetSetup(c *gin.Context) {
	setup := Setup{
		Status: constant.Setup,
	}
	// Refresh the process-local hint from the durable marker so a process that
	// started before another instance completed bootstrap does not advertise a
	// stale "not initialized" state.
	if !setup.Status {
		marker, err := model.GetSetupWithError()
		if err != nil {
			// A failed read is not evidence that the database is empty.  Returning
			// 503 keeps the bootstrap endpoint fail-closed during an outage and
			// avoids exposing a misleading first-run form.
			c.JSON(503, gin.H{"success": false, "message": "系统状态暂不可用"})
			return
		}
		if marker != nil {
			constant.Setup = true
			setup.Status = true
		}
	}
	if constant.Setup {
		c.JSON(200, gin.H{
			"success": true,
			"data":    setup,
		})
		return
	}
	rootExists, err := model.RootUserExistsWithError()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "系统状态暂不可用"})
		return
	}
	setup.RootInit = rootExists
	setup.DatabaseType = string(common.MainDatabaseType())
	c.JSON(200, gin.H{
		"success": true,
		"data":    setup,
	})
}

func PostSetup(c *gin.Context) {
	// constant.Setup is process-local and cannot fence a second application
	// instance. Consult the durable setup marker as well; InitializeSetup
	// performs the final cross-process compare-and-swap inside one transaction.
	marker, markerErr := model.GetSetupWithError()
	if markerErr != nil {
		c.JSON(503, gin.H{"success": false, "message": "系统状态暂不可用"})
		return
	}
	if constant.Setup || marker != nil {
		constant.Setup = true
		c.JSON(200, gin.H{
			"success": false,
			"message": "系统已经初始化完成",
		})
		return
	}

	// This is only a UX hint. The root-user decision is repeated inside the
	// initialization transaction so a concurrent request cannot leave a half
	// initialized database.
	rootExists, err := model.RootUserExistsWithError()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "系统状态暂不可用"})
		return
	}

	var req SetupRequest
	err = c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": "请求参数有误",
		})
		return
	}

	// If root doesn't exist, validate and create admin account
	if !rootExists {
		// Validate username length: max 12 characters to align with model.User validation
		if len(req.Username) > 12 {
			c.JSON(200, gin.H{
				"success": false,
				"message": "用户名长度不能超过12个字符",
			})
			return
		}
		// Validate password
		if req.Password != req.ConfirmPassword {
			c.JSON(200, gin.H{
				"success": false,
				"message": "两次输入的密码不一致",
			})
			return
		}

		if len(req.Password) < 8 {
			c.JSON(200, gin.H{
				"success": false,
				"message": "密码长度至少为8个字符",
			})
			return
		}

	}

	// Hashing is intentionally done before entering the database transaction;
	// InitializeSetup only uses it if the transaction wins and no root exists.
	var hashedPassword string
	if !rootExists {
		hashedPassword, err = common.Password2Hash(req.Password)
		if err != nil {
			c.JSON(200, gin.H{
				"success": false,
				"message": "系统错误",
			})
			return
		}
	}

	initialized, err := model.InitializeSetup(model.SetupInitParams{
		Username:           req.Username,
		HashedPassword:     hashedPassword,
		SelfUseModeEnabled: req.SelfUseModeEnabled,
		DemoSiteEnabled:    req.DemoSiteEnabled,
		Version:            common.Version,
	})
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": "系统初始化失败",
		})
		return
	}
	if !initialized {
		constant.Setup = true
		c.JSON(200, gin.H{
			"success": false,
			"message": "系统已经初始化完成",
		})
		return
	}
	constant.Setup = true

	c.JSON(200, gin.H{
		"success": true,
		"message": "系统初始化成功",
	})
}
