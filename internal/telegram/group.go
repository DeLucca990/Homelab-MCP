package telegram

import (
	"strings"

	"github.com/go-telegram/bot/models"
)

// addressed reports whether msg is for the bot (command, @mention or reply to it)
// and returns its text without the mention.
func addressed(msg *models.Message, username string, botID int64) (string, bool) {
	text := strings.TrimSpace(msg.Text)
	if msg.Chat.Type == models.ChatTypePrivate {
		return text, true
	}

	if name, target := parseCommand(text); name != "" {
		return text, target == "" || strings.EqualFold(target, username)
	}

	if username != "" {
		mention := "@" + strings.ToLower(username)
		if i := strings.Index(strings.ToLower(text), mention); i >= 0 {
			return strings.TrimSpace(text[:i] + text[i+len(mention):]), true
		}
	}

	if r := msg.ReplyToMessage; r != nil && r.From != nil && r.From.ID == botID {
		return text, true
	}
	return "", false
}

func speaker(msg *models.Message) string {
	if msg.From == nil {
		return "?"
	}
	if name := strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName); name != "" {
		return name
	}
	return msg.From.Username
}
