package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
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

	headliner *headliner // nil when natural language is off: the digest goes without a headline
	digestMu  sync.Mutex // one digest at a time, so two never read and write the state at once
	ledger    *Ledger    // what every model response cost, for /cost and the daily budget
}

func New(cfg Config) (*Bot, error) {
	b := &Bot{cfg: cfg, approvals: NewApprovals(cfg.ApprovalTimeout)}
	b.mcp = NewMCP(cfg.MCPURL, cfg.MCPToken, b.approvals.Elicit)
	if cfg.LLM {
		b.agent = NewAgent(cfg, b.mcp)
		b.headliner = newHeadliner(cfg.DigestModel)
	}

	ledger, err := OpenLedger(cfg.LedgerPath, time.Now())
	if err != nil {
		log.Printf("spending ledger: %v — keeping it in memory, so a restart forgets what today cost", err)
		ledger = &Ledger{}
	}
	b.ledger = ledger

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
		cmds = append(cmds, models.BotCommand{Command: c.name, Description: c.menuDescription()})
	}
	if _, err := b.tg.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: cmds}); err != nil {
		log.Printf("telegram: could not publish the command list: %v", err)
	}

	mode := "commands only (no Anthropic credential)"
	if b.agent != nil {
		mode = "commands and natural language with " + b.cfg.Model + ", effort " + b.cfg.Effort
	}
	log.Printf("telegram bot running for %d user(s), %d of them admin, MCP at %s: %s",
		len(b.cfg.AllowedUsers), len(b.cfg.AdminUsers), b.cfg.MCPURL, mode)
	if len(b.cfg.AdminUsers) == 0 {
		log.Printf("%s is not set: removals, restarts and configuration changes are refused for everyone", AdminUsersEnv)
	}

	budget := "no daily budget"
	if b.cfg.DailyBudget > 0 {
		budget = fmt.Sprintf("a daily budget of $%.2f", b.cfg.DailyBudget)
	}
	log.Printf("spending kept in %s, with %s", b.cfg.LedgerPath, budget)

	switch {
	case b.cfg.DigestAt == nil:
		log.Printf("daily digest off (%s=off)", DigestAtEnv)
	case len(b.cfg.DigestChats) == 0:
		log.Printf("daily digest off: nobody to send it to — set %s or %s", AdminUsersEnv, DigestChatsEnv)
	default:
		headline := "a headline by " + b.cfg.DigestModel
		if b.headliner == nil {
			headline = "no headline (no Anthropic credential)"
		}
		log.Printf("daily digest at %s to %d chat(s), with %s, totals kept in %s",
			b.cfg.DigestAt, len(b.cfg.DigestChats), headline, b.cfg.DigestStatePath)
		go b.runDigests(ctx)
	}

	go b.heartbeat(ctx)
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
	admin := b.cfg.IsAdmin(msg.From.ID)

	name, _, rest := parseCommand(text)
	switch name {
	case "":
		if msg.Chat.Type != models.ChatTypePrivate {
			text = speaker(msg) + ": " + text
		}
		b.ask(ctx, chatID, msg.From.ID, speaker(msg), text)
	case "start", "help":
		b.sendHTML(ctx, chatID, helpText(b.agent != nil, admin))
	case "reset":
		if b.agent != nil {
			b.agent.Reset(chatID)
		}
		b.sendHTML(ctx, chatID, "Conversa reiniciada.")
	case "cost":
		b.sendHTML(ctx, chatID, costReport(b.ledger, time.Now(), msg.From.ID, admin, b.cfg.DailyBudget))
	case "digest":
		log.Printf("%s → /digest", who(requester{chatID: chatID, userID: msg.From.ID}))
		stop := b.typing(ctx, chatID)
		b.sendDigest(ctx, []int64{chatID}, false)
		stop()
	default:
		cmd, ok := findCommand(name)
		if !ok {
			b.sendHTML(ctx, chatID, helpText(b.agent != nil, admin))
			return
		}
		b.runCommand(ctx, chatID, msg.From.ID, cmd, rest)
	}
}

func (b *Bot) runCommand(ctx context.Context, chatID, userID int64, cmd command, rest string) {
	var args map[string]any
	if cmd.args != nil {
		var err error
		if args, err = cmd.args(rest); err != nil {
			b.sendHTML(ctx, chatID, "⚠️ "+escape(err.Error())+"\n\nUso: <code>"+escape(cmd.usageLine())+"</code>")
			return
		}
	}

	stop := b.typing(ctx, chatID)
	defer stop()

	admin := b.cfg.IsAdmin(userID)
	ctx = withRequester(ctx, chatID, userID, admin)
	r := requesterOf(ctx)
	ran := 0
	for _, name := range cmd.tools {
		tool, err := b.mcp.Tool(ctx, name)
		if err != nil {
			b.sendError(ctx, chatID, err)
			return
		}
		if tool == nil {
			continue
		}
		ran++

		if !admin && adminOnly(tool) {
			log.Printf("%s → %s refused: admin only", who(r), name)
			b.sendHTML(ctx, chatID, "/"+cmd.name+" é só para os administradores deste bot.")
			continue
		}

		log.Printf("%s → %s", who(r), name)
		text, isError, err := b.mcp.Call(ctx, name, args)
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

func (b *Bot) ask(ctx context.Context, chatID, userID int64, name, text string) {
	if b.agent == nil {
		b.sendHTML(ctx, chatID, helpText(false, b.cfg.IsAdmin(userID)))
		return
	}
	if spent, over := b.budgetSpent(time.Now()); over {
		log.Printf("%s: refused, the daily budget is spent ($%.4f of $%.2f)",
			who(requester{chatID: chatID, userID: userID}), spent, b.cfg.DailyBudget)
		b.sendHTML(ctx, chatID, budgetMessage(b.cfg.DailyBudget, spent))
		return
	}

	stop := b.typing(ctx, chatID)
	answer, used, err := b.agent.Ask(ctx, chatID, userID, text)
	stop()
	if used.steps > 0 {
		b.record(entryOf(time.Now(), kindQuestion, chatID, userID, name, used, err != nil))
	}

	switch {
	case errors.Is(err, errBusy):
		b.sendHTML(ctx, chatID, "Ainda estou no pedido anterior — talvez esperando uma aprovação sua.")
	case err != nil:
		log.Printf("chat %d: %v", chatID, err)
		message := "⚠️ " + escape(err.Error())
		if used.steps > 0 {
			message += "\n\n" + usageLine(used, "gasto antes da falha")
		}
		b.sendHTML(ctx, chatID, message)
	default:
		b.sendReply(ctx, chatID, answer+"\n\n"+usageLine(used, ""))
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
