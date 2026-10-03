package telegram

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	TokenEnv           = "TELEGRAM_BOT_TOKEN"
	AllowedUsersEnv    = "TELEGRAM_ALLOWED_USER_IDS"
	AllowedChatsEnv    = "TELEGRAM_ALLOWED_CHAT_IDS"
	MCPURLEnv          = "HOMELAB_MCP_URL"
	ModelEnv           = "TELEGRAM_MODEL"
	EffortEnv          = "TELEGRAM_EFFORT"
	ApprovalTimeoutEnv = "TELEGRAM_APPROVAL_TIMEOUT"

	mcpAddrEnv  = "HOMELAB_MCP_HTTP_ADDR"
	mcpTokenEnv = "HOMELAB_MCP_HTTP_TOKEN"

	anthropicKeyEnv   = "ANTHROPIC_API_KEY"
	anthropicTokenEnv = "ANTHROPIC_AUTH_TOKEN"
)

const (
	defaultModel           = "claude-sonnet-5-5"
	defaultEffort          = "medium"
	defaultApprovalTimeout = 2 * time.Minute
)

var efforts = []string{"low", "medium", "high", "xhigh"}

type Config struct {
	Token        string
	AllowedUsers map[int64]bool

	AllowedChats map[int64]bool

	MCPURL   string
	MCPToken string

	LLM    bool
	Model  string
	Effort string

	ApprovalTimeout time.Duration
}

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

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

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
