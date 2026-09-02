package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type wechatLoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func newWeChatHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// The request carries WeChatServerToken in Authorization. Never
			// resend that credential to a redirect target.
			return http.ErrUseLastResponse
		},
	}
}

func getWeChatIdByCode(code string, securityConfig common.SecurityRuntimeConfig) (string, error) {
	if code == "" {
		return "", errors.New("无效的参数")
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/wechat/user?code=%s", securityConfig.WeChatServerAddress, url.QueryEscape(code)), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", securityConfig.WeChatServerToken)
	client := newWeChatHTTPClient(5 * time.Second)
	httpResponse, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer httpResponse.Body.Close()
	var res wechatLoginResponse
	err = common.DecodeJsonLimited(httpResponse.Body, service.DefaultProviderResponseBodyLimitBytes, &res)
	if err != nil {
		return "", err
	}
	if !res.Success {
		return "", errors.New(res.Message)
	}
	if res.Data == "" {
		return "", errors.New("验证码错误或已过期")
	}
	return res.Data, nil
}

func WeChatAuth(c *gin.Context) {
	securityConfig := common.GetSecurityRuntimeConfig()
	if !securityConfig.WeChatAuthEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "管理员未开启通过微信登录以及注册",
			"success": false,
		})
		return
	}
	code := c.Query("code")
	wechatId, err := getWeChatIdByCode(code, securityConfig)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"message": common.MaskSensitiveInfo(err.Error()),
			"success": false,
		})
		return
	}
	user := model.User{
		WeChatId: wechatId,
	}
	lookupErr := user.FillUserByWeChatId()
	if lookupErr == nil {
		if user.Id <= 0 {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户已注销"})
			return
		}
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		// Do not treat a database outage as an unclaimed identity: doing so
		// could create a second account or race a binding operation.
		common.ApiError(c, lookupErr)
		return
	} else {
		if securityConfig.RegisterEnabled {
			user.Username = "wechat_" + strconv.Itoa(model.GetMaxUserId()+1)
			user.DisplayName = "WeChat User"
			user.Role = common.RoleCommonUser
			user.Status = common.UserStatusEnabled

			// Register the user and durable identity claim atomically. The
			// users-table lookup above is only a fast path and can race another
			// WeChat callback.
			err := model.DB.Transaction(func(tx *gorm.DB) error {
				if err := user.InsertWithTx(tx, 0); err != nil {
					return err
				}
				return model.ClaimExternalIdentityWithTx(tx, model.ExternalIdentityProviderWeChat, wechatId, user.Id)
			})
			if err != nil {
				if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) {
					// Another callback won the claim. Continue with that existing
					// account rather than leaving a provisional duplicate user.
					user = model.User{WeChatId: wechatId}
					if lookupErr := user.FillUserByWeChatId(); lookupErr == nil && user.Id > 0 {
						if user.Status != common.UserStatusEnabled {
							c.JSON(http.StatusOK, gin.H{"message": "用户已被封禁", "success": false})
							return
						}
						setupLogin(&user, c)
						return
					} else if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
						common.ApiError(c, lookupErr)
						return
					}
				}
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": common.MaskSensitiveInfo(err.Error()),
				})
				return
			}
			user.FinalizeOAuthUserCreation(0)
		} else {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "管理员关闭了新用户注册",
			})
			return
		}
	}

	if user.Status != common.UserStatusEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "用户已被封禁",
			"success": false,
		})
		return
	}
	setupLogin(&user, c)
}

type wechatBindRequest struct {
	Code string `json:"code"`
}

func WeChatBind(c *gin.Context) {
	securityConfig := common.GetSecurityRuntimeConfig()
	if !securityConfig.WeChatAuthEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "管理员未开启通过微信登录以及注册",
			"success": false,
		})
		return
	}
	var req wechatBindRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "无效的请求",
		})
		return
	}
	code := req.Code
	wechatId, err := getWeChatIdByCode(code, securityConfig)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"message": common.MaskSensitiveInfo(err.Error()),
			"success": false,
		})
		return
	}
	userId := c.GetInt("id")
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	// Claim and update the binding column in one transaction. The claim's
	// subject/user uniqueness closes races with another bind or registration.
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		// A bind is a replacement operation. Release the old subject only
		// inside this transaction so a failed claim preserves the old binding.
		if err := model.ReleaseExternalIdentityWithTx(tx, model.ExternalIdentityProviderWeChat, userId); err != nil {
			return err
		}
		if err := model.ClaimExternalIdentityWithTx(tx, model.ExternalIdentityProviderWeChat, wechatId, userId); err != nil {
			return err
		}
		// 只更新绑定列，避免完整用户快照覆盖并发的封禁、降权或分组变更。
		return model.UpdateUserBindColumnWithTx(tx, userId, "wechat_id", wechatId)
	}); err != nil {
		if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "该微信账号已被绑定",
			})
			return
		}
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
	return
}
