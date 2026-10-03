package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Bot struct {
	cfg       Config
	mcp       *MCP
	agent     *Agent // nil when natural language is off
	approvals *Approvals
	tg        *bot.Bot
	username  string // the bot's own, to recognise a mention in a group
}

func New(cfg Config) (*Bot, error) {
	b := &Bot{cfg: cfg, approvals: NewApprovals(cfg.ApprovalTimeout)}
	b.mcp = NewMCP(cfg.MCPURL, cfg.MCPToken, b.approvals.Elicit)
	if cfg.LLM {
		b.agent = NewAgent(cfg, b.mcp)
	}

	tg, err := bot.New(cfg.Token,
		bot.WithMiddlewares(b.authorize),
		bot.WithDefaultHandler(b.handle),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "callback_query"}),
		bot.WithErrorsHandler(func(err error) { log.Printf("telegram: %v", err) }),
	)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w (is %s the token @BotFather gave?)", err, TokenEnv)
	}
	b.tg = tg
	b.approvals.SetMessenger(tg)

	me, err := tg.GetMe(context.Background())
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	b.username = me.Username
	return b, nil
}

func (b *Bot) Run(ctx context.Context) {
	defer b.mcp.Close()

	var cmds []models.BotCommand
	for _, c := range append(toolCommands, otherCommands...) {
		cmds = append(cmds, models.BotCommand{Command: c.name, Description: c.description})
	}
	if _, err := b.tg.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: cmds}); err != nil {
		log.Printf("telegram: could not publish the command list: %v", err)
	}

	mode := "commands only (no Anthropic credential)"
	if b.agent != nil {
		mode = "commands and natural language with " + b.cfg.Model + ", effort " + b.cfg.Effort
	}
	log.Printf("telegram bot running for %d user(s), MCP at %s: %s", len(b.cfg.AllowedUsers), b.cfg.MCPURL, mode)

	b.tg.Start(ctx)
}

// Unauthorised updates are dropped silently: a reply would confirm the bot exists.
func (b *Bot) authorize(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, tg *bot.Bot, u *models.Update) {
		var userID int64
		switch {
		case u.Message != nil && u.Message.From != nil:
			if _, ok := addressed(u.Message, b.username, tg.ID()); !ok {
				return
			}
			if chat := u.Message.Chat; chat.Type != models.ChatTypePrivate && !b.cfg.AllowedChats[chat.ID] {
				log.Printf("telegram: ignored a message in %s chat %d (%q), which is not in %s",
					chat.Type, chat.ID, chat.Title, AllowedChatsEnv)
				return
			}
			userID = u.Message.From.ID
		case u.CallbackQuery != nil:
			userID = u.CallbackQuery.From.ID
		default:
			return
		}

		if !b.cfg.AllowedUsers[userID] {
			log.Printf("telegram: ignored an update from user %d, who is not in %s", userID, AllowedUsersEnv)
			return
		}
		next(ctx, tg, u)
	}
}

func (b *Bot) handle(ctx context.Context, _ *bot.Bot, u *models.Update) {
	switch {
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.Text != "":
		b.onText(ctx, u.Message)
	}
}

func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	text := "Botão desconhecido."
	if strings.HasPrefix(q.Data, callbackPrefix) {
		text = b.approvals.Answer(q.From.ID, q.Data)
	}
	if _, err := b.tg.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: q.ID,
		Text:            text,
	}); err != nil {
		log.Printf("telegram: answering a button press: %v", err)
	}
}

func (b *Bot) onText(ctx context.Context, msg *models.Message) {
	chatID := msg.Chat.ID
	text, _ := addressed(msg, b.username, b.tg.ID())

	switch name, _ := parseCommand(text); name {
	case "":
		if msg.Chat.Type != models.ChatTypePrivate {
			text = speaker(msg) + ": " + text
		}
		b.ask(ctx, chatID, msg.From.ID, text)
	case "start", "help":
		b.sendHTML(ctx, chatID, helpText(b.agent != nil))
	case "reset":
		if b.agent != nil {
			b.agent.Reset(chatID)
		}
		b.sendHTML(ctx, chatID, "Conversa reiniciada.")
	default:
		cmd, ok := findCommand(name)
		if !ok {
			b.sendHTML(ctx, chatID, helpText(b.agent != nil))
			return
		}
		b.runCommand(ctx, chatID, msg.From.ID, cmd)
	}
}

func (b *Bot) runCommand(ctx context.Context, chatID, userID int64, cmd command) {
	stop := b.typing(ctx, chatID)
	defer stop()

	ctx = withRequester(ctx, chatID, userID)
	ran := 0
	for _, name := range cmd.tools {
		ok, err := b.mcp.HasTool(ctx, name)
		if err != nil {
			b.sendError(ctx, chatID, err)
			return
		}
		if !ok {
			continue
		}
		ran++

		text, isError, err := b.mcp.Call(ctx, name, nil)
		if err != nil {
			b.sendError(ctx, chatID, err)
			continue
		}
		if isError {
			text = "⚠️ " + text
		}
		b.sendOutput(ctx, chatID, name, text)
	}

	if ran == 0 {
		b.sendHTML(ctx, chatID, "O servidor não tem "+escape(strings.Join(cmd.tools, " nem "))+
			" registrado — o serviço não está configurado no ambiente dele.")
	}
}

func (b *Bot) ask(ctx context.Context, chatID, userID int64, text string) {
	if b.agent == nil {
		b.sendHTML(ctx, chatID, helpText(false))
		return
	}

	stop := b.typing(ctx, chatID)
	answer, err := b.agent.Ask(ctx, chatID, userID, text)
	stop()

	switch {
	case errors.Is(err, errBusy):
		b.sendHTML(ctx, chatID, "Ainda estou no pedido anterior — talvez esperando uma aprovação sua.")
	case err != nil:
		b.sendError(ctx, chatID, err)
	default:
		b.sendReply(ctx, chatID, answer)
	}
}

// Telegram clears the typing indicator after 5s, so it is renewed.
func (b *Bot) typing(ctx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			b.tg.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return cancel
}

func (b *Bot) sendOutput(ctx context.Context, chatID int64, tool, text string) {
	chunks := chunk(text, chunkBudget)
	if len(chunks) > maxChunks {
		if _, err := b.tg.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID:   chatID,
			Document: &models.InputFileUpload{Filename: tool + ".txt", Data: strings.NewReader(text)},
			Caption:  tool,
		}); err != nil {
			log.Printf("telegram: sending %s as a file: %v", tool, err)
		}
		return
	}
	for _, c := range chunks {
		b.sendHTML(ctx, chatID, pre(c))
	}
}

// If Telegram still rejects the markup, resend without it.
func (b *Bot) sendReply(ctx context.Context, chatID int64, text string) {
	for _, c := range chunkHTML(parseHTML(text), chunkBudget) {
		if err := b.send(ctx, chatID, c, models.ParseModeHTML); err != nil {
			log.Printf("telegram: reply markup rejected, sending plain: %v", err)
			b.send(ctx, chatID, plainText(parseHTML(c)), "")
		}
	}
}

func (b *Bot) sendError(ctx context.Context, chatID int64, err error) {
	log.Printf("chat %d: %v", chatID, err)
	b.sendHTML(ctx, chatID, "⚠️ "+escape(err.Error()))
}

func (b *Bot) sendHTML(ctx context.Context, chatID int64, text string) {
	if err := b.send(ctx, chatID, text, models.ParseModeHTML); err != nil {
		log.Printf("telegram: sending to chat %d: %v", chatID, err)
	}
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, mode models.ParseMode) error {
	disabled := true
	_, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          mode,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
	})
	return err
}
