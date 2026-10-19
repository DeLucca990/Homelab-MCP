package telegram

import (
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	TokenEnv           = "TELEGRAM_BOT_TOKEN"
	AllowedUsersEnv    = "TELEGRAM_ALLOWED_USER_IDS"
	AdminUsersEnv      = "TELEGRAM_ADMIN_USER_IDS"
	AllowedChatsEnv    = "TELEGRAM_ALLOWED_CHAT_IDS"
	MCPURLEnv          = "HOMELAB_MCP_URL"
	ModelEnv           = "TELEGRAM_MODEL"
	EffortEnv          = "TELEGRAM_EFFORT"
	ApprovalTimeoutEnv = "TELEGRAM_APPROVAL_TIMEOUT"
	DigestAtEnv        = "TELEGRAM_DIGEST_AT"
	DigestChatsEnv     = "TELEGRAM_DIGEST_CHAT_IDS"
	DigestModelEnv     = "TELEGRAM_DIGEST_MODEL"
	DigestStatePathEnv = "TELEGRAM_DIGEST_STATE_PATH"
	DailyBudgetEnv     = "TELEGRAM_DAILY_BUDGET_USD"
	LedgerPathEnv      = "TELEGRAM_LEDGER_PATH"

	mcpAddrEnv  = "HOMELAB_MCP_HTTP_ADDR"
	mcpTokenEnv = "HOMELAB_MCP_HTTP_TOKEN"

	anthropicKeyEnv   = "ANTHROPIC_API_KEY"
	anthropicTokenEnv = "ANTHROPIC_AUTH_TOKEN"
)

const (
	defaultModel           = "claude-sonnet-5-5"
	defaultEffort          = "medium"
	defaultApprovalTimeout = 2 * time.Minute
	defaultDigestAt        = "17:00"
	defaultDigestModel     = "claude-haiku-5-5"
)

var efforts = []string{"low", "medium", "high", "xhigh"}

type Config struct {
	Token        string
	AllowedUsers map[int64]bool
	AdminUsers   map[int64]bool

	AllowedChats map[int64]bool

	MCPURL   string
	MCPToken string

	LLM    bool
	Model  string
	Effort string

	ApprovalTimeout time.Duration

	DigestAt        *DailyAt
	DigestChats     []int64
	DigestModel     string
	DigestStatePath string

	LedgerPath  string
	DailyBudget float64
}

type DailyAt struct{ Hour, Minute int }

func (d DailyAt) String() string { return fmt.Sprintf("%02d:%02d", d.Hour, d.Minute) }

// LoadConfig reports every problem at once.
func LoadConfig() (Config, error) {
	var problems []string

	cfg := Config{
		Token:           env(TokenEnv),
		MCPToken:        env(mcpTokenEnv),
		Model:           envOr(ModelEnv, defaultModel),
		Effort:          strings.ToLower(envOr(EffortEnv, defaultEffort)),
		ApprovalTimeout: defaultApprovalTimeout,
		LLM:             env(anthropicKeyEnv) != "" || env(anthropicTokenEnv) != "",
		DigestModel:     envOr(DigestModelEnv, defaultDigestModel),
	}

	if cfg.Token == "" {
		problems = append(problems, fmt.Sprintf("%s is not set: it is the token @BotFather gave when the bot was created", TokenEnv))
	}

	users, err := parseUserIDs(env(AllowedUsersEnv))
	switch {
	case err != nil:
		problems = append(problems, fmt.Sprintf("%s: %v", AllowedUsersEnv, err))
	case len(users) == 0:
		problems = append(problems, fmt.Sprintf(
			"%s is not set: list the numeric Telegram user ids allowed to use the bot, "+
				"comma-separated. Without it anyone who finds the bot could act on this server",
			AllowedUsersEnv))
	}
	cfg.AllowedUsers = users

	admins, err := parseUserIDs(env(AdminUsersEnv))
	if err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", AdminUsersEnv, err))
	}
	if len(users) > 0 {
		var strays []string
		for id := range admins {
			if !users[id] {
				strays = append(strays, strconv.FormatInt(id, 10))
			}
		}
		if len(strays) > 0 {
			slices.Sort(strays)
			problems = append(problems, fmt.Sprintf(
				"%s lists %s, not in %s: an admin must also be allowed to use the bot",
				AdminUsersEnv, strings.Join(strays, ", "), AllowedUsersEnv))
		}
	}
	cfg.AdminUsers = admins

	chats, err := parseChatIDs(env(AllowedChatsEnv))
	if err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", AllowedChatsEnv, err))
	}
	cfg.AllowedChats = chats

	cfg.MCPURL = env(MCPURLEnv)
	if cfg.MCPURL == "" {
		if addr := env(mcpAddrEnv); addr != "" {
			cfg.MCPURL = "http://" + addr + "/mcp"
			if host, _, err := net.SplitHostPort(addr); err == nil && isLoopback(host) {
				log.Printf("%s is unset, so the server is reached at %s — the server's own "+
					"listen address, which is this machine only if the bot runs on the server's. "+
					"Elsewhere, set %s to the address that serves it over the tailnet",
					MCPURLEnv, cfg.MCPURL, MCPURLEnv)
			}
		}
	}
	if cfg.MCPURL == "" {
		problems = append(problems, fmt.Sprintf(
			"%s is not set, and neither is %s to derive it from: the bot needs the "+
				"server's endpoint, e.g. http://100.101.102.103:3000/mcp",
			MCPURLEnv, mcpAddrEnv))
	}

	if cfg.MCPToken == "" {
		problems = append(problems, fmt.Sprintf("%s is not set: the bot sends the same bearer token as any other client", mcpTokenEnv))
	}

	if !contains(efforts, cfg.Effort) {
		problems = append(problems, fmt.Sprintf("%s=%q: must be one of %s", EffortEnv, cfg.Effort, strings.Join(efforts, ", ")))
	}

	if raw := env(ApprovalTimeoutEnv); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			problems = append(problems, fmt.Sprintf("%s=%q: must be a positive duration such as 2m", ApprovalTimeoutEnv, raw))
		} else {
			cfg.ApprovalTimeout = d
		}
	}

	problems = append(problems, cfg.loadDigest()...)
	problems = append(problems, cfg.loadFiles()...)

	if raw := env(DailyBudgetEnv); raw != "" {
		v, err := strconv.ParseFloat(strings.Replace(raw, ",", ".", 1), 64)
		if err != nil || !(v > 0) || math.IsInf(v, 0) {
			problems = append(problems, fmt.Sprintf("%s=%q: must be a positive amount of dollars such as 1.50", DailyBudgetEnv, raw))
		} else {
			cfg.DailyBudget = v
		}
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func (c *Config) loadDigest() []string {
	var problems []string

	switch raw := strings.ToLower(envOr(DigestAtEnv, defaultDigestAt)); raw {
	case "off":
	default:
		t, err := time.Parse("15:04", raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s=%q: must be a time of day such as 17:00, or off", DigestAtEnv, raw))
			break
		}
		c.DigestAt = &DailyAt{Hour: t.Hour(), Minute: t.Minute()}
	}

	if raw := env(DigestChatsEnv); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			switch {
			case err != nil || id == 0:
				problems = append(problems, fmt.Sprintf("%s: %q is not a chat id", DigestChatsEnv, part))
			case id > 0 && !c.AllowedUsers[id], id < 0 && !c.AllowedChats[id]:
				problems = append(problems, fmt.Sprintf(
					"%s lists %d, which is in neither %s nor %s: the digest would send the "+
						"server's state to a chat the bot otherwise ignores",
					DigestChatsEnv, id, AllowedUsersEnv, AllowedChatsEnv))
			default:
				c.DigestChats = append(c.DigestChats, id)
			}
		}
	} else {
		for id := range c.AdminUsers {
			c.DigestChats = append(c.DigestChats, id)
		}
		slices.Sort(c.DigestChats)
	}

	return problems
}

func (c *Config) loadFiles() []string {
	c.DigestStatePath, c.LedgerPath = env(DigestStatePathEnv), env(LedgerPathEnv)
	if c.DigestStatePath != "" && c.LedgerPath != "" {
		return nil
	}
	dir, err := stateDir()
	if err != nil {
		return []string{fmt.Sprintf("there is no directory to keep the bot's files in (%v): "+
			"set %s and %s to file paths", err, DigestStatePathEnv, LedgerPathEnv)}
	}
	if c.DigestStatePath == "" {
		c.DigestStatePath = filepath.Join(dir, "digest.json")
	}
	if c.LedgerPath == "" {
		c.LedgerPath = filepath.Join(dir, "spend.jsonl")
	}
	return nil
}

func stateDir() (string, error) {
	if dirs := env("STATE_DIRECTORY"); dirs != "" {
		dir, _, _ := strings.Cut(dirs, ":")
		return dir, nil
	}
	if dir := env("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "homelab-telegram-bot"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "homelab-telegram-bot"), nil
}

func (c Config) IsAdmin(userID int64) bool { return c.AdminUsers[userID] }

func parseUserIDs(raw string) (map[int64]bool, error) {
	ids := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a numeric user id — usernames change hands, ids do not", part)
		}
		ids[id] = true
	}
	return ids, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Group ids are negative; the bot logs the id of every group it ignores.
func parseChatIDs(raw string) (map[int64]bool, error) {
	ids := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id >= 0 {
			return nil, fmt.Errorf("%q is not a group id — those are negative numbers, as the bot logs them", part)
		}
		ids[id] = true
	}
	return ids, nil
}

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func envOr(key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
