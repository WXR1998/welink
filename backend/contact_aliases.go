package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func initContactAliasTables() error {
	_, err := aiDB.Exec(`CREATE TABLE IF NOT EXISTS mem_contact_aliases (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		contact_key TEXT    NOT NULL,
		alias       TEXT    NOT NULL,
		created_at  INTEGER NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("mem_contact_aliases: create table: %w", err)
	}
	_, err = aiDB.Exec(`CREATE INDEX IF NOT EXISTS idx_mem_contact_aliases_key ON mem_contact_aliases(contact_key)`)
	if err != nil {
		return fmt.Errorf("mem_contact_aliases: create index: %w", err)
	}
	_, err = aiDB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_mem_contact_aliases ON mem_contact_aliases(contact_key, alias)`)
	if err != nil {
		return fmt.Errorf("mem_contact_aliases: create unique index: %w", err)
	}
	return nil
}

type ContactAlias struct {
	ID         int64  `json:"id"`
	ContactKey string `json:"contact_key"`
	Alias      string `json:"alias"`
}

func aliasDB() *sql.DB {
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	return aiDB
}

func ListContactAliases(contactKey string) ([]string, error) {
	db := aliasDB()
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`SELECT alias FROM mem_contact_aliases WHERE contact_key = ? ORDER BY id`, contactKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			continue
		}
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func GetAllContactAliases() (map[string][]string, error) {
	db := aliasDB()
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`SELECT contact_key, alias FROM mem_contact_aliases ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var key, alias string
		if err := rows.Scan(&key, &alias); err != nil {
			continue
		}
		alias = strings.TrimSpace(alias)
		if alias == "" || !strings.HasPrefix(key, "contact:") {
			continue
		}
		out[key] = append(out[key], alias)
	}
	return out, nil
}

func AddContactAlias(contactKey, alias string) (int64, error) {
	db := aliasDB()
	if db == nil {
		return 0, fmt.Errorf("AI DB 未就绪")
	}
	contactKey = strings.TrimSpace(contactKey)
	alias = strings.TrimSpace(alias)
	res, err := db.Exec(
		`INSERT INTO mem_contact_aliases(contact_key, alias, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(contact_key, alias) DO NOTHING`,
		contactKey, alias, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func DeleteContactAlias(contactKey, alias string) error {
	db := aliasDB()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}
	_, err := db.Exec(`DELETE FROM mem_contact_aliases WHERE contact_key = ? AND alias = ?`, contactKey, alias)
	return err
}

func DeleteContactAliases(contactKey string) error {
	db := aliasDB()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}
	_, err := db.Exec(`DELETE FROM mem_contact_aliases WHERE contact_key = ?`, contactKey)
	return err
}

// PinnedContactAlias 描述“被注入置顶记忆”的联系人及其外号，供最终 LLM 上下文使用。
type PinnedContactAlias struct {
	ContactKey  string   `json:"contact_key"`
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases"`
}

// buildPinnedContactAliases 根据置顶记忆的来源接触点，返回“注入置顶记忆的联系人及其外号”。
// displayName 为空时调用方用可读来源名；这里不依赖 ContactService，仅处理 contact_key。
func buildPinnedContactAliases(pinned []MemFact, displayName func(string) string) []PinnedContactAlias {
	all, err := GetAllContactAliases()
	if err != nil {
		return nil
	}
	return collectPinnedAliases(pinned, all, displayName)
}

// collectPinnedAliases 是 buildPinnedContactAliases 的纯逻辑拆分，便于单测。
func collectPinnedAliases(pinned []MemFact, all map[string][]string, displayName func(string) string) []PinnedContactAlias {
	if len(all) == 0 || len(pinned) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []PinnedContactAlias
	for _, f := range pinned {
		key := f.ContactKey
		if !strings.HasPrefix(key, "contact:") || seen[key] {
			continue
		}
		aliases := all[key]
		if len(aliases) == 0 {
			continue
		}
		seen[key] = true
		name := ""
		if displayName != nil {
			name = displayName(key)
		}
		out = append(out, PinnedContactAlias{ContactKey: key, DisplayName: name, Aliases: aliases})
	}
	return out
}

// registerContactAliasRoutes 挂载联系人外号管理端点（/api/memory/contact-aliases）。
func registerContactAliasRoutes(api *gin.RouterGroup) {
	// GET /api/memory/contact-aliases?contact_key=contact:xxx
	api.GET("/memory/contact-aliases", func(c *gin.Context) {
		key := strings.TrimSpace(c.Query("contact_key"))
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contact_key 必填"})
			return
		}
		aliases, err := ListContactAliases(key)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if aliases == nil {
			aliases = []string{}
		}
		c.JSON(http.StatusOK, gin.H{"aliases": aliases})
	})

	// POST /api/memory/contact-aliases  body: {contact_key, alias}
	api.POST("/memory/contact-aliases", func(c *gin.Context) {
		var body struct {
			ContactKey string `json:"contact_key"`
			Alias      string `json:"alias"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
			return
		}
		key := strings.TrimSpace(body.ContactKey)
		alias := strings.TrimSpace(body.Alias)
		if key == "" || alias == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contact_key / alias 不能为空"})
			return
		}
		_, err := AddContactAlias(key, alias)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// DELETE /api/memory/contact-aliases?contact_key=&alias=
	api.DELETE("/memory/contact-aliases", func(c *gin.Context) {
		key := strings.TrimSpace(c.Query("contact_key"))
		alias := strings.TrimSpace(c.Query("alias"))
		if key == "" || alias == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contact_key / alias 不能为空"})
			return
		}
		if err := DeleteContactAlias(key, alias); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
