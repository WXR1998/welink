package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// prefsMu 串行化所有 preferences 的"读-改-写"。
// preferences.json 是个 60+ 字段的大结构（含全部 OAuth token / API key），
// 多个 handler + 后台 token 刷新会并发改它；没有锁的话 last-write-wins 会丢凭据，
// 配合非原子写还可能写坏文件。所有走 updatePreferences() 的修改都在这把锁下进行。
var prefsMu sync.Mutex

// resolveDownloadDir 返回当前应该写入导出文件的目录：
//  1. 用户在设置里配置的 DownloadDir（存在且可写）
//  2. 平台默认：$HOME/Downloads（Mac/Win）或 $XDG_DOWNLOAD_DIR（Linux）
//
// 目录必须在用户 home 之下（防止前端传入 /etc 之类触发任意写）。
func resolveDownloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法获取用户目录：%w", err)
	}

	pick := func(dir string) (string, error) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("解析目录失败：%w", err)
		}
		if !strings.HasPrefix(abs, home+string(filepath.Separator)) && abs != home {
			return "", fmt.Errorf("下载目录必须在用户目录 %s 之下：%s", home, abs)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", fmt.Errorf("创建目录失败：%w", err)
		}
		// 写权限探测
		probe := filepath.Join(abs, ".welink_write_probe")
		if f, err := os.Create(probe); err != nil {
			return "", fmt.Errorf("目录不可写：%s（%v）", abs, err)
		} else {
			f.Close()
			os.Remove(probe)
		}
		return abs, nil
	}

	p := loadPreferences()
	if strings.TrimSpace(p.DownloadDir) != "" {
		return pick(p.DownloadDir)
	}
	// 平台默认
	if xdg := os.Getenv("XDG_DOWNLOAD_DIR"); xdg != "" {
		return pick(xdg)
	}
	return pick(filepath.Join(home, "Downloads"))
}

// migrateConfigYAML 一次性迁移：仅 App 模式下，如果 config.yaml 存在，打印警告提示迁移。
// Docker 模式下 config.yaml 仍然被正常使用，不打印迁移提示。
func migrateConfigYAML() {
	if appPreferencesDir() == "" {
		return // 非 App 模式（Docker/CLI），config.yaml 正常使用，不提示迁移
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		log.Printf("[MIGRATE] Found config.yaml — please migrate settings to the Settings page. config.yaml is no longer used.")
	}
}

// DataDirProfile 单个数据目录配置项（用于多账号切换）。
type DataDirProfile struct {
	ID            string `json:"id"`                        // 短 UUID，前端用作 key
	Name          string `json:"name"`                      // 用户起的别名，如「主号」「老婆账号」
	Path          string `json:"path"`                      // 解密后 decrypted/ 目录的绝对路径
	LastIndexedAt int64  `json:"last_indexed_at,omitempty"` // 上次成功索引的 Unix 秒
}

// ImageProfile 单个文生图配置项，支持多 provider 并行配置。
// 第一条 = 默认；其它条作为备选，调用 GenerateImage 时可显式传 profile_id 切换。
type ImageProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"` // doubao / openai / siliconflow / gemini
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
}

// EmbeddingProfile 是单个 Embedding 提供商配置。
type EmbeddingProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"` // ollama/openai/jina/custom
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
	Dims     int    `json:"dims,omitempty"`
}

// MemLLMProfile 是单个记忆提炼 LLM 提供商配置。
type MemLLMProfile struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	APIKey          string `json:"api_key,omitempty"`
	BaseURL         string `json:"base_url,omitempty"`
	Model           string `json:"model,omitempty"`
	UseResponsesAPI bool   `json:"use_responses_api,omitempty"`
	FastMode        bool   `json:"fast_mode,omitempty"`
}

// RerankProfile 是单个 Rerank（重排）提供商配置。
type RerankProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"` // jina/cohere/siliconflow/custom
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
}

// LLMProfile 单个 LLM 配置项，支持多 provider 并行配置与一键切换。
type LLMProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
	NoThink  bool   `json:"no_think,omitempty"` // Ollama 思考型模型（Qwen3+）专用：开启后在消息前加 /no_think 跳过推理
	// 深度思考档位：off（默认）/ low / medium / high。
	//   - Claude (Sonnet 4+ / Opus 4+)  → 映射为 thinking.budget_tokens（2K/8K/16K）
	//   - OpenAI o-series / gpt-5-reasoning → reasoning_effort 字段
	//   - DeepSeek R1 / Ollama qwen3 等     → 模型自带 <think>，本字段不影响
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// 上下文窗口大小（token 数），用于自动压缩对话历史。0 = 使用默认值 128000。
	ContextWindow int `json:"context_window,omitempty"`
	// 上下文压缩阈值（token 数）。对话 token 数超过此值时触发压缩。
	// 0 = 使用 context_window 的 70%（给输出和检索注入预留空间）。
	CompressThreshold int `json:"compress_threshold,omitempty"`
	// 使用 OpenAI Responses API（POST /responses）而不是 Chat Completions。
	// 适用于仅支持 Responses API 的 OpenAI 兼容网关。
	UseResponsesAPI bool `json:"use_responses_api,omitempty"`
	// FastMode 对原生 OpenAI 和自定义兼容接口发送 service_tier=priority。
	FastMode bool `json:"fast_mode,omitempty"`
}

// AIQALLMProfiles 指定跨联系人问答各 LLM 步骤的可选模型。
// 留空时跟随前端当前选择的问答模型，保持旧版单模型行为。
type AIQALLMProfiles struct {
	QueryDecomposition string `json:"query_decomposition,omitempty"`
	QueryExpansion     string `json:"query_expansion,omitempty"`
	FinalAnswer        string `json:"final_answer,omitempty"`
}

// AIQAStepModels 是一次跨联系人问答各 LLM 步骤实际使用的模型名。
type AIQAStepModels struct {
	QueryDecomposition     string `json:"query_decomposition,omitempty"`
	QueryExpansion         string `json:"query_expansion,omitempty"`
	FinalAnswer            string `json:"final_answer,omitempty"`
	QueryDecompositionFast bool   `json:"query_decomposition_fast,omitempty"`
	QueryExpansionFast     bool   `json:"query_expansion_fast,omitempty"`
	FinalAnswerFast        bool   `json:"final_answer_fast,omitempty"`
}

// aiQAStepProfileID 返回问答步骤应使用的 profile。
// 仅接受仍存在的步骤覆盖配置；失效或空配置一律回退到当前问答请求的 profile。
func aiQAStepProfileID(prefs Preferences, step, requestProfileID string) string {
	var configuredID string
	switch step {
	case "query_decomposition":
		configuredID = prefs.AIQALLMProfiles.QueryDecomposition
	case "query_expansion":
		configuredID = prefs.AIQALLMProfiles.QueryExpansion
	case "final_answer":
		configuredID = prefs.AIQALLMProfiles.FinalAnswer
	}
	for _, profile := range prefs.LLMProfiles {
		if profile.ID == configuredID {
			return configuredID
		}
	}
	return requestProfileID
}

func aiQAStepModelNames(prefs Preferences, requestProfileID string) AIQAStepModels {
	configFor := func(step string) llmConfig {
		profileID := aiQAStepProfileID(prefs, step, requestProfileID)
		return llmConfigForProfile(profileID, prefs)
	}
	decomposition := configFor("query_decomposition")
	expansion := configFor("query_expansion")
	finalAnswer := configFor("final_answer")
	return AIQAStepModels{
		QueryDecomposition:     decomposition.model,
		QueryExpansion:         expansion.model,
		FinalAnswer:            finalAnswer.model,
		QueryDecompositionFast: decomposition.fastMode && supportsFastServiceTier(decomposition.provider),
		QueryExpansionFast:     expansion.fastMode && supportsFastServiceTier(expansion.provider),
		FinalAnswerFast:        finalAnswer.fastMode && supportsFastServiceTier(finalAnswer.provider),
	}
}

// sanitizeAIQALLMProfiles 移除指向已删除 LLM profile 的步骤覆盖配置。
func sanitizeAIQALLMProfiles(profiles AIQALLMProfiles, llmProfiles []LLMProfile) AIQALLMProfiles {
	valid := make(map[string]struct{}, len(llmProfiles))
	for _, profile := range llmProfiles {
		valid[profile.ID] = struct{}{}
	}
	if _, ok := valid[profiles.QueryDecomposition]; !ok {
		profiles.QueryDecomposition = ""
	}
	if _, ok := valid[profiles.QueryExpansion]; !ok {
		profiles.QueryExpansion = ""
	}
	if _, ok := valid[profiles.FinalAnswer]; !ok {
		profiles.FinalAnswer = ""
	}
	return profiles
}

// Preferences 是唯一的持久化结构体，合并了用户偏好和 App 配置。
// App 模式存储在 ~/Library/Application Support/WeLink/preferences.json，
// Docker/CLI 模式存储路径由环境变量 PREFERENCES_PATH 指定，默认为工作目录的 preferences.json。
// CurrentSchemaVersion 是当前代码理解的 preferences.json 格式版本。
// 每次做带破坏性语义的改动（字段语义翻转、删除、合并）时 +1，并在 migratePreferences 加对应 case。
// 只加字段且 zero-value 兼容的改动不用升级版本。
//
// v2: ImageProvider/ImageAPIKey/ImageBaseURL/ImageModel 单字段 → ImageProfiles 数组。
// v3: 顶层 LLM 连接参数 → 选中的默认 LLM profile。
// v4: 顶层 Embedding / 记忆提炼 / Rerank 连接参数 → 对应 profiles。
// v5: Embedding / 记忆提炼 / Rerank 从顺序 fallback 改为显式选择一个 profile。
// v6: 全局 openai_fast_mode 迁移为每个 LLM profile 的 fast_mode。
const CurrentSchemaVersion = 6

type Preferences struct {
	// 0 或缺失 = 旧版本（需要迁移）；>= CurrentSchemaVersion = 当前版本
	SchemaVersion int `json:"schema_version,omitempty"`

	// App 模式专用
	DataDir     string `json:"data_dir,omitempty"`
	LogDir      string `json:"log_dir,omitempty"`
	DownloadDir string `json:"download_dir,omitempty"` // 导出图片/文件的保存目录；留空 = 平台默认（~/Downloads）
	DemoMode    bool   `json:"demo_mode,omitempty"`
	// 多账号 / 多数据目录支持。当前激活的目录（DataDir）一定也存在于这个列表中。
	DataDirProfiles []DataDirProfile `json:"data_dir_profiles,omitempty"`

	// 服务器配置（修改后需重启）
	Port    string `json:"port,omitempty"`     // 默认 8080，环境变量 PORT 覆盖
	GinMode string `json:"gin_mode,omitempty"` // debug / release

	// 分析参数（支持热加载）
	Timezone             string `json:"timezone,omitempty"`                // 默认 Asia/Shanghai
	LateNightStartHour   int    `json:"late_night_start_hour,omitempty"`   // 默认 0
	LateNightEndHour     int    `json:"late_night_end_hour,omitempty"`     // 默认 5
	SessionGapSeconds    int64  `json:"session_gap_seconds,omitempty"`     // 默认 21600（6 小时）
	WorkerCount          int    `json:"worker_count,omitempty"`            // 默认 4
	LateNightMinMessages int64  `json:"late_night_min_messages,omitempty"` // 默认 100
	LateNightTopN        int    `json:"late_night_top_n,omitempty"`        // 默认 20
	DefaultInitFrom      int64  `json:"default_init_from,omitempty"`
	DefaultInitTo        int64  `json:"default_init_to,omitempty"`
	AnalysisCompleted    bool   `json:"analysis_completed,omitempty"`

	// 日志配置（支持热加载）
	LogLevel string `json:"log_level,omitempty"` // debug / info / warn / error，默认 info

	// 两种模式通用
	BlockedUsers  []string `json:"blocked_users"`
	BlockedGroups []string `json:"blocked_groups"`
	PrivacyMode   bool     `json:"privacy_mode,omitempty"`

	// 飞书 bot：每个飞书群（chat_id）只允许问答访问的白名单 contact_key。
	// 只做白名单（含群 group:xxx 与私聊 contact:xxx，一视同仁）。
	// 未配置的飞书群 = 默认放行（兼容现状）。
	FeishuGroupScope map[string][]string `json:"feishu_group_scope,omitempty"` // chat_id -> allowed contact_keys

	// 飞书 bot 枚举到的群列表（chat_id -> 群名），供前端下拉展示。
	FeishuBotChats map[string]string `json:"feishu_bot_chats,omitempty"` // chat_id -> chat name

	// 飞书 bot：每个飞书群（chat_id）补充给 AI 的提示信息，注入到 system prompt。
	FeishuGroupPrompts map[string]string `json:"feishu_group_prompts,omitempty"` // chat_id -> prompt text

	// 屏幕锁定（纯前端覆盖层，微信 PC Cmd+L 同思路）
	// PIN 用 bcrypt 哈希（salt 内嵌），由后端 /api/lock/* 负责验证
	LockPinHash     string `json:"lock_pin_hash,omitempty"`
	AutoLockMinutes int    `json:"auto_lock_minutes,omitempty"` // 0=关闭自动锁；30/60/120
	LockOnStartup   bool   `json:"lock_on_startup,omitempty"`   // App 重开是否默认锁定

	// 播客功能 TTS 配置（NotebookLM 风格音频回顾）
	// 默认走 OpenAI TTS；base URL 留空 = https://api.openai.com/v1
	PodcastTTSBaseURL string `json:"podcast_tts_base_url,omitempty"`
	PodcastTTSAPIKey  string `json:"podcast_tts_api_key,omitempty"`
	PodcastTTSModel   string `json:"podcast_tts_model,omitempty"`   // 默认 tts-1
	PodcastTTSVoiceA  string `json:"podcast_tts_voice_a,omitempty"` // 主持人 A，默认 alloy
	PodcastTTSVoiceB  string `json:"podcast_tts_voice_b,omitempty"` // 主持人 B，默认 nova

	// 关系预测「不再推荐此人」名单（仍可在联系人/群聊中看到，只是首页 forecast 不再提醒）
	ForecastIgnored []string `json:"forecast_ignored,omitempty"`

	// 「暧昧探测」Lab 排除名单（标记真伴侣/家人/客户等不参与暧昧统计的人）
	FlirtExcluded []string `json:"flirt_excluded,omitempty"`

	// LLM 连接参数只存在于 LLMProfiles；顶层只记录默认选中的 profile。
	LLMProfiles         []LLMProfile    `json:"llm_profiles,omitempty"`
	DefaultLLMProfileID string          `json:"default_llm_profile_id,omitempty"`
	AIQALLMProfiles     AIQALLMProfiles `json:"ai_qa_llm_profiles,omitempty"`
	AIAnalysisDBPath    string          `json:"ai_analysis_db_path,omitempty"` // 留空 = 与 preferences.json 同目录

	EmbeddingProfiles         []EmbeddingProfile `json:"embedding_profiles,omitempty"`
	DefaultEmbeddingProfileID string             `json:"default_embedding_profile_id,omitempty"`

	// 文生图配置（年报封面 / 高光插画 / AI 头像等场景）
	// 默认 disabled — 生图比文本贵 10-50 倍，必须用户主动开启 + 主动点按钮触发
	ImageEnabled  bool           `json:"image_enabled,omitempty"`
	ImageProfiles []ImageProfile `json:"image_profiles,omitempty"`
	// 以下单字段保持向后兼容（migration 会同步为 ImageProfiles[0]，新代码读 ImageProfiles）
	ImageProvider string `json:"image_provider,omitempty"` // doubao 等；默认 doubao（火山方舟即梦）
	ImageAPIKey   string `json:"image_api_key,omitempty"`
	ImageBaseURL  string `json:"image_base_url,omitempty"`
	ImageModel    string `json:"image_model,omitempty"`

	// 向量检索缓存（内存）
	VecCacheMaxKeys int `json:"vec_cache_max_keys,omitempty"` // 最多缓存几个联系人的 embedding，0 = 默认 3

	// 记忆提炼 profile；为空时复用默认 LLM profile。
	MemLLMProfiles         []MemLLMProfile `json:"mem_llm_profiles,omitempty"`
	DefaultMemLLMProfileID string          `json:"default_mem_llm_profile_id,omitempty"`

	// Rerank profile；为空时不启用。
	RerankProfiles         []RerankProfile `json:"rerank_profiles,omitempty"`
	DefaultRerankProfileID string          `json:"default_rerank_profile_id,omitempty"`

	// 自定义纪念日
	CustomAnniversaries []CustomAnniversary `json:"custom_anniversaries,omitempty"`

	// 旧版 Prompt 模板暂存字段。启动时会迁移进 AI SQLite 数据库并清空。
	PromptTemplates map[string]string `json:"prompt_templates,omitempty"`

	// 导出中心：第三方笔记/文档平台令牌
	NotionToken       string `json:"notion_token,omitempty"`        // Notion Integration Token (secret_xxx)
	NotionParentPage  string `json:"notion_parent_page,omitempty"`  // 默认上传到的 Page ID（也可在导出时覆盖）
	FeishuAppID       string `json:"feishu_app_id,omitempty"`       // 飞书自建应用 App ID
	FeishuAppSecret   string `json:"feishu_app_secret,omitempty"`   // 飞书自建应用 App Secret
	FeishuFolderToken string `json:"feishu_folder_token,omitempty"` // 默认导入到的文件夹 Token（留空 = 我的空间根目录）

	// 导出中心：云盘 / 对象存储
	// WebDAV（坚果云 / Nextcloud / ownCloud / 群晖等）
	WebDAVURL      string `json:"webdav_url,omitempty"` // 完整 URL，例 https://dav.jianguoyun.com/dav/
	WebDAVUsername string `json:"webdav_username,omitempty"`
	WebDAVPassword string `json:"webdav_password,omitempty"` // 应用密码
	WebDAVPath     string `json:"webdav_path,omitempty"`     // 上传前缀，例 WeLink-Export/

	// S3 兼容（AWS S3 / Cloudflare R2 / 阿里 OSS / 腾讯 COS / 七牛 / MinIO / Backblaze）
	S3Endpoint     string `json:"s3_endpoint,omitempty"` // 主机名，空=AWS 官方；自定义端点用于国内云
	S3Region       string `json:"s3_region,omitempty"`
	S3Bucket       string `json:"s3_bucket,omitempty"`
	S3AccessKey    string `json:"s3_access_key,omitempty"`
	S3SecretKey    string `json:"s3_secret_key,omitempty"`
	S3PathPrefix   string `json:"s3_path_prefix,omitempty"`    // 上传前缀，例 welink-export/
	S3UsePathStyle bool   `json:"s3_use_path_style,omitempty"` // true=path-style（MinIO/R2），false=virtual-host（AWS 官方默认）

	// Dropbox（用 App Console 生成的长期 access token）
	DropboxToken string `json:"dropbox_token,omitempty"`
	DropboxPath  string `json:"dropbox_path,omitempty"` // 上传前缀，例 /Apps/WeLink/

	// Google Drive（OAuth 2.0，本地回调）
	GDriveClientID     string `json:"gdrive_client_id,omitempty"`
	GDriveClientSecret string `json:"gdrive_client_secret,omitempty"`
	GDriveAccessToken  string `json:"gdrive_access_token,omitempty"`
	GDriveRefreshToken string `json:"gdrive_refresh_token,omitempty"`
	GDriveTokenExpiry  int64  `json:"gdrive_token_expiry,omitempty"`
	GDriveFolderID     string `json:"gdrive_folder_id,omitempty"` // 留空=根目录

	// OneDrive（OAuth 2.0，Microsoft Identity Platform）
	OneDriveClientID     string `json:"onedrive_client_id,omitempty"`
	OneDriveClientSecret string `json:"onedrive_client_secret,omitempty"`
	OneDriveTenant       string `json:"onedrive_tenant,omitempty"` // 一般填 common
	OneDriveAccessToken  string `json:"onedrive_access_token,omitempty"`
	OneDriveRefreshToken string `json:"onedrive_refresh_token,omitempty"`
	OneDriveTokenExpiry  int64  `json:"onedrive_token_expiry,omitempty"`
	OneDriveFolderPath   string `json:"onedrive_folder_path,omitempty"` // 例 /WeLink-Export

	// 移动端配对：启用后，外部（非同源）请求必须带 Bearer token 才能访问 API。
	// 目的是让手机 App 安全地远程连上 PC 上的 WeLink 后端。
	// 空字符串 = 未启用（向后兼容：所有请求像以前一样放行）。
	MobilePairingToken string `json:"mobile_pairing_token,omitempty"`

	// Gemini OAuth（可选，与 API Key 二选一）
	GeminiClientID     string `json:"gemini_client_id,omitempty"`
	GeminiClientSecret string `json:"gemini_client_secret,omitempty"`
	GeminiAccessToken  string `json:"gemini_access_token,omitempty"`
	GeminiRefreshToken string `json:"gemini_refresh_token,omitempty"`
	GeminiTokenExpiry  int64  `json:"gemini_token_expiry,omitempty"` // Unix timestamp
}

// CustomAnniversary 用户自定义纪念日
type CustomAnniversary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Date      string `json:"date"`               // YYYY-MM-DD
	Recurring bool   `json:"recurring"`          // 每年重复
	Username  string `json:"username,omitempty"` // 可选关联联系人
}

// preferencesPath 返回 preferences.json 的绝对路径。
// 优先级：环境变量 PREFERENCES_PATH > 默认路径。
func preferencesPath() string {
	if v := os.Getenv("PREFERENCES_PATH"); v != "" {
		return v
	}
	if hasFrontend {
		return filepath.Join(appPreferencesDir(), "preferences.json")
	}
	return "preferences.json"
}

// loadPreferences 从磁盘读取偏好；文件不存在时返回空结构体。
func loadPreferences() Preferences {
	data, err := os.ReadFile(preferencesPath())
	if err != nil {
		return defaultPreferences()
	}
	p, err := decodePreferences(data)
	if err != nil {
		log.Printf("[PREFS] Failed to parse preferences.json: %v", err)
		return defaultPreferences()
	}
	if p.BlockedUsers == nil {
		p.BlockedUsers = []string{}
	}
	if p.BlockedGroups == nil {
		p.BlockedGroups = []string{}
	}
	// 运行迁移并按需写回。迁移只在版本落后时真正做事；就位时 0 开销。
	migrated := migratePreferences(p)
	if migrated.SchemaVersion != p.SchemaVersion {
		log.Printf("[PREFS] schema v%d → v%d 迁移完成", p.SchemaVersion, migrated.SchemaVersion)
		// 用 TryLock 区分两种调用上下文，避免重入死锁：
		//   - 锁空闲：是普通 loadPreferences 调用，拿到锁直接写回。
		//   - 锁已被持有：说明正处在 updatePreferences 内（loadPreferences 是它的一步），
		//     无需在此写回——调用方的 savePreferencesLocked 会把 migrated 一起持久化。
		if prefsMu.TryLock() {
			if err := savePreferencesLocked(migrated); err != nil {
				log.Printf("[PREFS] 迁移后写回失败，不影响运行：%v", err)
			}
			prefsMu.Unlock()
		}
	}
	return migrated
}

// legacyLLMFields 仅用于读取 v3 之前的单 LLM 配置，写回时不会保留这些顶层字段。
type legacyLLMFields struct {
	Provider          string `json:"llm_provider"`
	APIKey            string `json:"llm_api_key"`
	BaseURL           string `json:"llm_base_url"`
	Model             string `json:"llm_model"`
	EmbeddingProvider string `json:"embedding_provider"`
	EmbeddingAPIKey   string `json:"embedding_api_key"`
	EmbeddingBaseURL  string `json:"embedding_base_url"`
	EmbeddingModel    string `json:"embedding_model"`
	EmbeddingDims     int    `json:"embedding_dims"`
	MemLLMBaseURL     string `json:"mem_llm_base_url"`
	MemLLMModel       string `json:"mem_llm_model"`
	MemLLMAPIKey      string `json:"mem_llm_api_key"`
	RerankProvider    string `json:"rerank_provider"`
	RerankAPIKey      string `json:"rerank_api_key"`
	RerankBaseURL     string `json:"rerank_base_url"`
	RerankModel       string `json:"rerank_model"`
	OpenAIFastMode    bool   `json:"openai_fast_mode"`
}

func decodePreferences(data []byte) (Preferences, error) {
	var p Preferences
	if err := json.Unmarshal(data, &p); err != nil {
		return Preferences{}, err
	}
	var legacy legacyLLMFields
	if err := json.Unmarshal(data, &legacy); err != nil {
		return Preferences{}, err
	}
	needsMigration := false
	migrationFrom := CurrentSchemaVersion
	markMigration := func(fromVersion int) {
		needsMigration = true
		if fromVersion < migrationFrom {
			migrationFrom = fromVersion
		}
	}
	if len(p.LLMProfiles) == 0 && legacy.Provider != "" {
		p.LLMProfiles = []LLMProfile{{
			ID: "llm-default", Name: legacy.Provider,
			Provider: legacy.Provider, APIKey: legacy.APIKey,
			BaseURL: legacy.BaseURL, Model: legacy.Model,
		}}
		markMigration(2)
	}
	if len(p.LLMProfiles) > 0 && p.DefaultLLMProfileID == "" {
		markMigration(2)
	}
	if len(p.EmbeddingProfiles) == 0 && legacy.EmbeddingProvider != "" {
		p.EmbeddingProfiles = []EmbeddingProfile{{
			ID: "embedding-default", Name: legacy.EmbeddingProvider,
			Provider: legacy.EmbeddingProvider, APIKey: legacy.EmbeddingAPIKey,
			BaseURL: legacy.EmbeddingBaseURL, Model: legacy.EmbeddingModel, Dims: legacy.EmbeddingDims,
		}}
		markMigration(4)
	}
	if len(p.EmbeddingProfiles) > 0 && !hasEmbeddingProfile(p.EmbeddingProfiles, p.DefaultEmbeddingProfileID) {
		markMigration(4)
	}
	if len(p.MemLLMProfiles) == 0 && (legacy.MemLLMBaseURL != "" || legacy.MemLLMModel != "" || legacy.MemLLMAPIKey != "") {
		provider := legacy.Provider
		if legacy.MemLLMAPIKey == "" {
			provider = "ollama"
		}
		p.MemLLMProfiles = []MemLLMProfile{{
			ID: "mem-default", Name: provider, Provider: provider, APIKey: legacy.MemLLMAPIKey,
			BaseURL: legacy.MemLLMBaseURL, Model: legacy.MemLLMModel,
		}}
		markMigration(4)
	}
	if len(p.MemLLMProfiles) > 0 && !hasMemLLMProfile(p.MemLLMProfiles, p.DefaultMemLLMProfileID) {
		markMigration(4)
	}
	if legacy.OpenAIFastMode {
		for i := range p.LLMProfiles {
			if supportsFastServiceTier(p.LLMProfiles[i].Provider) {
				p.LLMProfiles[i].FastMode = true
			}
		}
		for i := range p.MemLLMProfiles {
			if supportsFastServiceTier(p.MemLLMProfiles[i].Provider) {
				p.MemLLMProfiles[i].FastMode = true
			}
		}
		markMigration(5)
	}
	if len(p.RerankProfiles) == 0 && legacy.RerankProvider != "" {
		p.RerankProfiles = []RerankProfile{{
			ID: "rerank-default", Name: legacy.RerankProvider, Provider: legacy.RerankProvider,
			APIKey: legacy.RerankAPIKey, BaseURL: legacy.RerankBaseURL, Model: legacy.RerankModel,
		}}
		markMigration(4)
	}
	if len(p.RerankProfiles) > 0 && !hasRerankProfile(p.RerankProfiles, p.DefaultRerankProfileID) {
		markMigration(4)
	}
	if needsMigration && p.SchemaVersion > migrationFrom {
		p.SchemaVersion = migrationFrom
	}
	return p, nil
}

// defaultPreferences 返回带有最新 schema_version 的空配置（首次启动用）。
func defaultPreferences() Preferences {
	return Preferences{
		SchemaVersion: CurrentSchemaVersion,
		BlockedUsers:  []string{},
		BlockedGroups: []string{},
	}
}

func hasEmbeddingProfile(profiles []EmbeddingProfile, id string) bool {
	for _, profile := range profiles {
		if profile.ID == id {
			return true
		}
	}
	return false
}

func hasMemLLMProfile(profiles []MemLLMProfile, id string) bool {
	for _, profile := range profiles {
		if profile.ID == id {
			return true
		}
	}
	return false
}

func hasRerankProfile(profiles []RerankProfile, id string) bool {
	for _, profile := range profiles {
		if profile.ID == id {
			return true
		}
	}
	return false
}

// sanitizeForExport 返回用于导出的 Preferences 副本：
//   - 机器特定字段（绝对路径、数据目录列表）始终清空，因为换机器没法用
//   - stripSecrets=true 时，所有 API Key / OAuth token / 密码也清空
func sanitizeForExport(p Preferences, stripSecrets bool) Preferences {
	// 机器特定 —— 无论如何都清空
	p.DataDir = ""
	p.LogDir = ""
	p.DownloadDir = ""
	p.AIAnalysisDBPath = ""
	p.DataDirProfiles = nil

	if !stripSecrets {
		return p
	}

	// 屏幕锁定 PIN（跟随 API Key 一起被视为敏感）
	p.LockPinHash = ""

	// 播客 TTS API Key
	p.PodcastTTSAPIKey = ""

	// LLM / Embedding / Image
	p.ImageAPIKey = ""
	for i := range p.LLMProfiles {
		p.LLMProfiles[i].APIKey = ""
	}
	for i := range p.ImageProfiles {
		p.ImageProfiles[i].APIKey = ""
	}
	for i := range p.EmbeddingProfiles {
		p.EmbeddingProfiles[i].APIKey = ""
	}
	for i := range p.MemLLMProfiles {
		p.MemLLMProfiles[i].APIKey = ""
	}

	for i := range p.RerankProfiles {
		p.RerankProfiles[i].APIKey = ""
	}

	// 云笔记
	p.NotionToken = ""
	p.FeishuAppSecret = ""

	// 对象存储
	p.WebDAVPassword = ""
	p.S3SecretKey = ""
	p.DropboxToken = ""

	// OAuth — client secret + 动态 token 一起清（token 单独留着没意义）
	p.GDriveClientSecret = ""
	p.GDriveAccessToken = ""
	p.GDriveRefreshToken = ""
	p.GDriveTokenExpiry = 0

	p.OneDriveClientSecret = ""
	p.OneDriveAccessToken = ""
	p.OneDriveRefreshToken = ""
	p.OneDriveTokenExpiry = 0

	p.GeminiClientSecret = ""
	p.GeminiAccessToken = ""
	p.GeminiRefreshToken = ""
	p.GeminiTokenExpiry = 0

	// 移动端配对 token —— 拥有即全权访问，不能随导出泄露
	p.MobilePairingToken = ""

	return p
}

// collectSecrets 收集 Preferences 里所有非空的敏感值（API key / OAuth token / 密码 / PIN hash），
// 用于日志脱敏等场景——把这些值在导出文本里替换成 [REDACTED]。
// 字段清单与 sanitizeForExport 保持一致：新增凭据字段时两处都要补，避免日志里漏脱敏。
func collectSecrets(p Preferences) []string {
	candidates := []string{
		p.ImageAPIKey,
		p.PodcastTTSAPIKey,
		p.NotionToken,
		p.FeishuAppSecret,
		p.WebDAVPassword,
		p.S3SecretKey,
		p.DropboxToken,
		p.GDriveClientSecret, p.GDriveAccessToken, p.GDriveRefreshToken,
		p.OneDriveClientSecret, p.OneDriveAccessToken, p.OneDriveRefreshToken,
		p.GeminiClientSecret, p.GeminiAccessToken, p.GeminiRefreshToken,
		p.MobilePairingToken,
		p.LockPinHash,
	}
	for _, prof := range p.LLMProfiles {
		candidates = append(candidates, prof.APIKey)
	}
	for _, prof := range p.ImageProfiles {
		candidates = append(candidates, prof.APIKey)
	}
	for _, prof := range p.EmbeddingProfiles {
		candidates = append(candidates, prof.APIKey)
	}
	for _, prof := range p.MemLLMProfiles {
		candidates = append(candidates, prof.APIKey)
	}
	for _, prof := range p.RerankProfiles {
		candidates = append(candidates, prof.APIKey)
	}
	out := make([]string, 0, len(candidates))
	for _, s := range candidates {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// mergeImported 导入新配置时的合并策略：
//   - 非机器字段全部用 imported 覆盖（LLM / 偏好 / 凭证等）
//   - 机器特定字段保留当前值（新机器上为空，用户会被引导重选；老机器上已配好不丢）
func mergeImported(current, imported Preferences) Preferences {
	imported.DataDir = current.DataDir
	imported.LogDir = current.LogDir
	imported.DownloadDir = current.DownloadDir
	imported.AIAnalysisDBPath = current.AIAnalysisDBPath
	imported.DataDirProfiles = current.DataDirProfiles
	return imported
}

// migratePreferences 把老 schema 的 preferences 升到最新。每步 migration 只做
// "本版本加进来的破坏性改动"，按版本号 case-by-case。新加字段但 zero-value 兼容
// 的情况不需要在这里写东西；写在这里的都是语义翻转 / 字段合并 / 字段拆分之类。
func migratePreferences(p Preferences) Preferences {
	// v0 → v1：给第一次见到 schema_version 字段的老用户打版本号。
	// 目前还没有破坏性改动，这里只是落版本号，为将来升级预留入口。
	if p.SchemaVersion < 1 {
		p.SchemaVersion = 1
	}
	// v1 → v2：ImageProvider/ImageAPIKey/ImageBaseURL/ImageModel 单字段 → ImageProfiles 数组。
	// 把老用户原有的单字段同步成 ImageProfiles[0]；单字段保留兼容旧代码读取。
	if p.SchemaVersion < 2 {
		if len(p.ImageProfiles) == 0 && (p.ImageProvider != "" || p.ImageAPIKey != "") {
			p.ImageProfiles = []ImageProfile{{
				ID:       "img-default",
				Name:     "默认",
				Provider: p.ImageProvider,
				APIKey:   p.ImageAPIKey,
				BaseURL:  p.ImageBaseURL,
				Model:    p.ImageModel,
			}}
		}
		p.SchemaVersion = 2
	}
	// v2 → v3：不再持久化顶层 LLM 参数，默认模型由 profile ID 唯一确定。
	if p.SchemaVersion < 3 {
		if p.DefaultLLMProfileID == "" && len(p.LLMProfiles) > 0 {
			p.DefaultLLMProfileID = p.LLMProfiles[0].ID
		}
		p.SchemaVersion = 3
	}
	if p.SchemaVersion < 4 {
		p.SchemaVersion = 4
	}
	if p.SchemaVersion < 5 {
		if !hasEmbeddingProfile(p.EmbeddingProfiles, p.DefaultEmbeddingProfileID) && len(p.EmbeddingProfiles) > 0 {
			p.DefaultEmbeddingProfileID = p.EmbeddingProfiles[0].ID
		}
		if !hasMemLLMProfile(p.MemLLMProfiles, p.DefaultMemLLMProfileID) && len(p.MemLLMProfiles) > 0 {
			p.DefaultMemLLMProfileID = p.MemLLMProfiles[0].ID
		}
		if !hasRerankProfile(p.RerankProfiles, p.DefaultRerankProfileID) && len(p.RerankProfiles) > 0 {
			p.DefaultRerankProfileID = p.RerankProfiles[0].ID
		}
		p.SchemaVersion = 5
	}
	if p.SchemaVersion < 6 {
		p.SchemaVersion = 6
	}
	return p
}

// effectiveConfig 返回合并了默认值和环境变量覆盖的 Preferences。
func effectiveConfig(p Preferences) Preferences {
	if p.Port == "" {
		p.Port = "8080"
	}
	if p.Timezone == "" {
		p.Timezone = "Asia/Shanghai"
	}
	if p.LateNightEndHour == 0 {
		p.LateNightEndHour = 5
	}
	if p.SessionGapSeconds == 0 {
		p.SessionGapSeconds = 21600
	}
	if p.WorkerCount == 0 {
		p.WorkerCount = 4
	}
	if p.LateNightMinMessages == 0 {
		p.LateNightMinMessages = 100
	}
	if p.LateNightTopN == 0 {
		p.LateNightTopN = 20
	}
	if p.LogLevel == "" {
		p.LogLevel = "info"
	}
	if p.GinMode == "" {
		p.GinMode = "debug"
	}
	// 环境变量覆盖
	if v := os.Getenv("DATA_DIR"); v != "" {
		p.DataDir = v
	}
	if v := os.Getenv("PORT"); v != "" {
		p.Port = v
	}
	return p
}

// hasKeyPlaceholder 是 API 响应中用于标记"已设置 key"的占位符。
// 前端看到此值 → 显示"已保存"提示；保存时传回此值或空 → 后端保留原值。
const hasKeyPlaceholder = "__HAS_KEY__"

// sanitizeForResponse 返回去除敏感字段的 Preferences 副本，用于 API 响应。
// API Key 不返回脱敏值，只返回占位符标记是否已设置。
func sanitizeForResponse(p Preferences) Preferences {
	redact := func(s string) string {
		if s == "" {
			return ""
		}
		return hasKeyPlaceholder
	}
	out := p
	out.ImageAPIKey = redact(out.ImageAPIKey)
	out.GeminiClientSecret = redact(out.GeminiClientSecret)
	out.GeminiAccessToken = ""
	out.GeminiRefreshToken = ""
	out.NotionToken = redact(out.NotionToken)
	out.FeishuAppSecret = redact(out.FeishuAppSecret)
	out.WebDAVPassword = redact(out.WebDAVPassword)
	out.S3SecretKey = redact(out.S3SecretKey)
	out.DropboxToken = redact(out.DropboxToken)
	out.GDriveClientSecret = redact(out.GDriveClientSecret)
	out.GDriveAccessToken = ""
	out.GDriveRefreshToken = ""
	out.OneDriveClientSecret = redact(out.OneDriveClientSecret)
	out.OneDriveAccessToken = ""
	out.OneDriveRefreshToken = ""
	out.PodcastTTSAPIKey = redact(out.PodcastTTSAPIKey)
	out.MobilePairingToken = redact(out.MobilePairingToken)
	if len(out.LLMProfiles) > 0 {
		sanitized := make([]LLMProfile, len(out.LLMProfiles))
		copy(sanitized, out.LLMProfiles)
		for i := range sanitized {
			sanitized[i].APIKey = redact(sanitized[i].APIKey)
		}
		out.LLMProfiles = sanitized
	}
	if len(out.EmbeddingProfiles) > 0 {
		sanitized := make([]EmbeddingProfile, len(out.EmbeddingProfiles))
		copy(sanitized, out.EmbeddingProfiles)
		for i := range sanitized {
			sanitized[i].APIKey = redact(sanitized[i].APIKey)
		}
		out.EmbeddingProfiles = sanitized
	}
	if len(out.MemLLMProfiles) > 0 {
		sanitized := make([]MemLLMProfile, len(out.MemLLMProfiles))
		copy(sanitized, out.MemLLMProfiles)
		for i := range sanitized {
			sanitized[i].APIKey = redact(sanitized[i].APIKey)
		}
		out.MemLLMProfiles = sanitized
	}

	if len(out.RerankProfiles) > 0 {
		sanitized := make([]RerankProfile, len(out.RerankProfiles))
		copy(sanitized, out.RerankProfiles)
		for i := range sanitized {
			sanitized[i].APIKey = redact(sanitized[i].APIKey)
		}
		out.RerankProfiles = sanitized
	}
	if len(out.ImageProfiles) > 0 {
		sanitized := make([]ImageProfile, len(out.ImageProfiles))
		copy(sanitized, out.ImageProfiles)
		for i := range sanitized {
			sanitized[i].APIKey = redact(sanitized[i].APIKey)
		}
		out.ImageProfiles = sanitized
	}
	return out
}

// savePreferences 将偏好写入磁盘。
// savePreferences 原子写入 preferences.json：先写同目录临时文件并 fsync，
// 再 rename 覆盖。避免 os.WriteFile 的 O_TRUNC 在写到一半崩溃时留下损坏的空/截断文件
// （那会让 loadPreferences 回退默认值，丢失全部配置与凭据）。
func savePreferences(p Preferences) error {
	// 持锁保证任意时刻只有一个写者，配合下面的原子 rename，杜绝并发写互相截断/写坏文件。
	// updatePreferences 已持有 prefsMu，会走 savePreferencesLocked 避免重入死锁。
	prefsMu.Lock()
	defer prefsMu.Unlock()
	return savePreferencesLocked(p)
}

// savePreferencesLocked 是 savePreferences 的实现体，调用方必须已持有 prefsMu。
func savePreferencesLocked(p Preferences) error {
	path := preferencesPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".preferences-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 失败路径上清理临时文件；成功 rename 后 tmpName 已不存在，Remove 无害。
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// updatePreferences 在 prefsMu 锁下做一次完整的"读最新 → 修改 → 原子写"。
// 所有需要改 preferences 的地方都应走这里，而不是各自 load→改→save，
// 否则并发修改会互相覆盖（典型：用户在设置页改配置时，后台 OAuth token 刷新写盘）。
// mutate 在持锁期间被调用，拿到当前磁盘上的最新副本，原地修改即可。
func updatePreferences(mutate func(p *Preferences)) (Preferences, error) {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	p := loadPreferences()
	mutate(&p)
	if err := savePreferencesLocked(p); err != nil {
		return p, err
	}
	return p, nil
}
