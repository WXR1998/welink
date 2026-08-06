package main

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"welink/backend/service"
)

// 占位联系人 API：直接写入微信 contact.db（username 用 extra_ 前缀，无聊天记录），
// 并立即刷新 ContactService 内存缓存，让联系人列表 / 实体解析 / 来源名都能识别。
const extraContactKeyPrefix = "contact:extra_"

// registerExtraContactRoutes 挂载占位联系人管理端点。
func registerExtraContactRoutes(api *gin.RouterGroup, getSvc func() *service.ContactService) {
	// POST /api/memory/extra-contacts  body: {display_name, note}
	api.POST("/memory/extra-contacts", func(c *gin.Context) {
		var body struct {
			DisplayName string `json:"display_name"`
			Note        string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
			return
		}
		svc := getSvc()
		if svc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "联系人服务未就绪"})
			return
		}
		username, err := svc.AddExtraContact(body.DisplayName, body.Note)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "contact_key": "contact:" + username, "username": username})
	})

	// DELETE /api/memory/extra-contacts/:username
	api.DELETE("/memory/extra-contacts/:username", func(c *gin.Context) {
		username := strings.TrimSpace(c.Param("username"))
		username = strings.TrimPrefix(username, "contact:")
		if username == "" || !strings.HasPrefix(username, "extra_") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "只能是占位联系人"})
			return
		}
		svc := getSvc()
		if svc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "联系人服务未就绪"})
			return
		}
		// 清理占位联系人的记忆与外号。
		cleanupExtraContactData("contact:" + username)
		if err := svc.RemoveExtraContact(username); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}

// cleanupExtraContactData 删除占位联系人关联的记忆与外号（在 ai_analysis.db）。
func cleanupExtraContactData(contactKey string) {
	db := getAIDB()
	if db == nil {
		return
	}
	_, _ = db.Exec(`DELETE FROM mem_facts WHERE contact_key = ?`, contactKey)
	_, _ = db.Exec(`DELETE FROM mem_contact_aliases WHERE contact_key = ?`, contactKey)
}

// extraContactsBySvc 返回所有占位联系人（用于前端列表/来源名解析）。
func extraContactsBySvc(svc *service.ContactService) []ExtraContactInfo {
	var out []ExtraContactInfo
	if svc == nil {
		return out
	}
	for _, s := range svc.GetCachedStats() {
		if strings.HasPrefix(s.Username, "extra_") {
			out = append(out, ExtraContactInfo{
				ContactKey:  "contact:" + s.Username,
				DisplayName: s.Remark,
			})
		}
	}
	return out
}

// ExtraContactInfo 是占位联系人的轻量展示结构。
type ExtraContactInfo struct {
	ContactKey  string `json:"contact_key"`
	DisplayName string `json:"display_name"`
}
