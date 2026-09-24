package settings

// Well-known keys and their value shapes. Every one of these is editable in the UI.

const (
	KeyGeneral    = "general"
	KeyModelRoles = "models.roles"
	KeyContext    = "context"
	KeyWeb        = "web"
	KeyTelegram   = "telegram"
	KeyObsidian   = "obsidian"
	KeyMacOS      = "macos"
	KeyBrowser    = "browser"
	KeyAutonomy   = "autonomy"
	KeyOnboarding = "onboarding"
	KeyMemory     = "memory"
	KeyRuntime    = "runtime"
	KeyElevenLabs = "elevenlabs"
	KeyGuardrails = "guardrails"
)

type General struct {
	UserName string `json:"user_name"`
	Timezone string `json:"timezone"` // IANA, empty → system
	Language string `json:"language"` // preferred reply language, free text
	Locale   string `json:"locale"`
}

// ModelRoles maps roles to a model or model-list name.
type ModelRoles struct {
	Chat      string `json:"chat"`      // default agent model
	Fast      string `json:"fast"`      // cheap tasks: summaries, extraction, tool selection
	Embedding string `json:"embedding"` // embedding model
}

type Context struct {
	CompactAt float64 `json:"compact_at"` // fraction of the window that triggers compaction
	Target    float64 `json:"target"`     // fraction to compact down to
}

type Web struct {
	SearchOrder     []string `json:"search_order"` // provider ids in fallback order
	TavilyKey       string   `json:"tavily_key"`
	YandexKey       string   `json:"yandex_key"`
	YandexFolder    string   `json:"yandex_folder"`
	YandexRegion    string   `json:"yandex_region"`
	AnySearchKey    string   `json:"anysearch_key"`
	AnySearchURL    string   `json:"anysearch_url"`
	FlareSolverrURL string   `json:"flaresolverr_url"`
	TranslateKey    string   `json:"translate_key"` // Google Cloud Translation API key
	UserAgent       string   `json:"user_agent"`
	AllowPrivate    bool     `json:"allow_private"` // let web tools reach localhost/LAN addresses
}

type Telegram struct {
	Enabled  bool   `json:"enabled"`
	Token    string `json:"token"`
	OwnerID  int64  `json:"owner_id"`  // the single allowed user
	GroupID  int64  `json:"group_id"`  // forum supergroup for topics
	PairCode string `json:"pair_code"` // one-time code to bind the owner
	// PairExpires is when the pairing code stops working (unix seconds); zero means expired.
	PairExpires int64  `json:"pair_expires"`
	BotName     string `json:"bot_name"`
	// MirrorNotices sends agent notices (background results, briefings) to the DM as well as the UI.
	MirrorNotices bool `json:"mirror_notices"`
}

type Obsidian struct {
	VaultPath string `json:"vault_path"`
}

type MacOS struct {
	Notifications bool   `json:"notifications"`
	Sound         string `json:"sound"`
}

type Browser struct {
	Headless  bool   `json:"headless"`
	RemoteURL string `json:"remote_url"` // ws:// or http:// DevTools endpoint of a running Chrome
	ChromeBin string `json:"chrome_bin"`
}

type Autonomy struct {
	Enabled      bool `json:"enabled"`
	DreamEnabled bool `json:"dream_enabled"`
	AutoEvolve   bool `json:"auto_evolve"` // apply soul proposals without review
	// HireLimit is how many agents other agents may hire per week (each starts on probation); 0 forbids it.
	HireLimit int `json:"hire_limit"`
}

func DefaultAutonomy() Autonomy { return Autonomy{HireLimit: 3} }

type Memory struct {
	ProcessEvery int `json:"process_every_s"` // raw → facts cadence
	RawBatch     int `json:"raw_batch"`
	// Reflection turns facts into conclusions (see memory.Reflect): after ReflectAfter new facts in a bank
	// (0 = the default of 8) unless ReflectOff.
	ReflectOff   bool `json:"reflect_off"`
	ReflectAfter int  `json:"reflect_after"`
	// AutoMergeOff disables merging project/domain banks the model judges to be the same topic (see
	// memory.AutoMergeBanks), run after distillation creates new facts.
	AutoMergeOff bool `json:"auto_merge_off"`
	// Hints is free text from the user, shown to the model whenever memory distils, reflects or extracts
	// entities ("I am Danil; agent names are not people", "treat X as a project, not a person"…).
	Hints string `json:"hints"`
	// ProcessMin is how many raw messages must be waiting before a digest pass runs on its own (0 = the default
	// of 6); a smaller backlog is still digested once its oldest message is 20 minutes old.
	ProcessMin int `json:"process_min"`
	// Entity extraction (people, products, places… and their relations) runs on its own after EntitiesAfter new
	// facts in a bank (0 = the default of 8) unless EntitiesOff.
	EntitiesOff   bool `json:"entities_off"`
	EntitiesAfter int  `json:"entities_after"`
	// Deep analysis (memory.Analyze: patterns, hypotheses, trends, contradictions, duplicates, profile card) runs
	// on its own after AnalyzeAfter new facts in a bank (0 = the default of 12) unless AnalyzeOff.
	// BookmarksOff stops the memory jobs from bookmarking pages the agents fetched (see harvest.Bookmarks).
	BookmarksOff bool `json:"bookmarks_off"`
	// DigestOff / DigestDays: a briefing that sums up what memory learned, worked out and doubts, every DigestDays
	// days (0 = 7).
	// VerifyOff stops the memory loop from sending an agent to check unverified web facts and hypotheses.
	VerifyOff bool `json:"verify_off"`
	// AutoIngestOff stops new files in the inbox from being learned automatically (see ingest.Scan).
	AutoIngestOff bool `json:"auto_ingest_off"`
	DigestOff     bool `json:"digest_off"`
	DigestDays    int  `json:"digest_days"`
	AnalyzeOff    bool `json:"analyze_off"`
	AnalyzeAfter  int  `json:"analyze_after"`
}

type Onboarding struct {
	Done  bool   `json:"done"`
	Hints string `json:"hints"`
}

// Runtime tunes how hard PRISM drives the model server.
type Runtime struct {
	LLMConcurrency int `json:"llm_concurrency"` // simultaneous model calls across all agents (local models like 1–2)
}

func DefaultRuntime() Runtime { return Runtime{LLMConcurrency: 4} }

// Guardrails tunes the loop detector (internal/agent's loopGuard) and the extra iteration budget
// autonomous (cron/intent-fired) runs get — see runner.go. Defaults match the values that shipped
// hardcoded before these became configurable, so leaving this unset changes nothing.
type Guardrails struct {
	// ToolRepeatWarn/ToolRepeatAbort: an identical tool call (same name + arguments) warns the model after
	// this many repeats within its recent history, and hard-aborts the run after this many.
	ToolRepeatWarn  int `json:"tool_repeat_warn"`
	ToolRepeatAbort int `json:"tool_repeat_abort"`
	// TextRepeatAbort: a would-be-final answer caught stuck regenerating the same phrase aborts the run
	// after this many occurrences (lower than the tool threshold — regenerating a whole response is far
	// more expensive than retrying one call).
	TextRepeatAbort int `json:"text_repeat_abort"`
	// AutonomousBoostPct/AutonomousMaxIterations: a cron- or intent-fired run gets its agent's own
	// MaxIterations increased by this percentage (nobody is watching to ask for more time), capped here.
	AutonomousBoostPct      int `json:"autonomous_boost_pct"`
	AutonomousMaxIterations int `json:"autonomous_max_iterations"`
	// AutoRetryOff stops the engine from analysing a run that exhausted its budget and retrying it once with a
	// rewritten instruction (see agent/recover.go).
	AutoRetryOff bool `json:"auto_retry_off"`
}

func DefaultGuardrails() Guardrails {
	return Guardrails{ToolRepeatWarn: 3, ToolRepeatAbort: 5, TextRepeatAbort: 2, AutonomousBoostPct: 50, AutonomousMaxIterations: 60}
}

func DefaultContext() Context { return Context{CompactAt: 0.8, Target: 0.2} }

const (
	KeyRetention = "retention"
	KeyNotify    = "notifications"
)

// Retention sets how long logs and finished tasks are kept (0 = forever).
type Retention struct {
	LogsDays  int `json:"logs_days"`
	TasksDays int `json:"tasks_days"`
	// TaskStatuses picks which finished tasks the cleanup removes (done, failed, cancelled, partial).
	// Partial is off by default: those runs are resumable and often still need a decision.
	TaskStatuses []string `json:"task_statuses"`
}

func DefaultRetention() Retention {
	return Retention{LogsDays: 14, TasksDays: 30, TaskStatuses: []string{"done", "failed", "cancelled"}}
}

// NotifyKind controls one class of notification.
type NotifyKind struct {
	Show     bool `json:"show"`     // record it and show it in the UI
	External bool `json:"external"` // also push it to macOS / Telegram
}

type Notify struct {
	Cron     NotifyKind `json:"cron"`
	Intent   NotifyKind `json:"intent"`
	Ask      NotifyKind `json:"ask"`
	Error    NotifyKind `json:"error"`
	Proposal NotifyKind `json:"proposal"` // an agent proposes a change to another agent (Metis)
}

func DefaultNotify() Notify {
	return Notify{Cron: NotifyKind{Show: true}, Intent: NotifyKind{Show: true, External: true}, Ask: NotifyKind{Show: true}, Error: NotifyKind{Show: true, External: true},
		Proposal: NotifyKind{Show: true, External: true}}
}

// Of returns the preferences of a kind.
func (n Notify) Of(kind string) NotifyKind {
	switch kind {
	case "cron":
		return n.Cron
	case "intent":
		return n.Intent
	case "ask":
		return n.Ask
	case "error":
		return n.Error
	case "proposal":
		return n.Proposal
	}
	return NotifyKind{Show: true}
}

// ElevenLabs configures voice, sound effects and image generation (elevenlabs.io).
type ElevenLabs struct {
	APIKey  string `json:"api_key"`
	VoiceID string `json:"voice_id"` // default voice for tts_speak; empty → the account's first voice
	ModelID string `json:"model_id"` // eleven_flash_v2_5 (0.5 credit/char), eleven_multilingual_v2 (1 credit/char)…
	// MonthlyCap is the most credits PRISM may spend per billing cycle (0 = the plan's own limit). The free
	// plan is small; keep some for yourself.
	MonthlyCap int `json:"monthly_cap"`
	// ConfirmOver asks before speaking a text longer than this many characters, even when the tool is armed.
	ConfirmOver int    `json:"confirm_over"`
	ImageModel  string `json:"image_model"` // image_generate default; needs a Pro plan on ElevenLabs' side
}

func DefaultElevenLabs() ElevenLabs {
	return ElevenLabs{ModelID: "eleven_flash_v2_5", MonthlyCap: 8000, ConfirmOver: 600, ImageModel: "gemini-2.5-flash-image"}
}
