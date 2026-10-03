package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const callbackPrefix = "ap:"

type messenger interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
	SendPhoto(ctx context.Context, params *bot.SendPhotoParams) (*models.Message, error)
	EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error)
	EditMessageCaption(ctx context.Context, params *bot.EditMessageCaptionParams) (*models.Message, error)
}

type Approvals struct {
	tg      messenger
	timeout time.Duration

	mu      sync.Mutex
	pending map[string]*pendingApproval
}

type pendingApproval struct {
	userID   int64
	decision chan string
}

func NewApprovals(timeout time.Duration) *Approvals {
	return &Approvals{timeout: timeout, pending: map[string]*pendingApproval{}}
}

func (a *Approvals) SetMessenger(tg messenger) { a.tg = tg }

// The elicitation arrives from inside the MCP SDK; the context is the only way
// to know which chat and user it belongs to.
type requester struct {
	chatID int64
	userID int64
}

type requesterKey struct{}

func withRequester(ctx context.Context, chatID, userID int64) context.Context {
	return context.WithValue(ctx, requesterKey{}, requester{chatID: chatID, userID: userID})
}

// Anything short of a button press returns "cancel", which the server treats as no.
func (a *Approvals) Elicit(ctx context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	who, ok := ctx.Value(requesterKey{}).(requester)
	if !ok || a.tg == nil {
		return &sdk.ElicitResult{Action: "cancel"}, nil
	}
	if req.Params.Mode != "" && req.Params.Mode != "form" {
		return &sdk.ElicitResult{Action: "decline"}, nil
	}

	id, err := newApprovalID()
	if err != nil {
		return nil, err
	}

	p := &pendingApproval{userID: who.userID, decision: make(chan string, 1)}
	a.mu.Lock()
	a.pending[id] = p
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	question := formatApproval(req.Params.Message)
	msg, isPhoto, err := a.ask(ctx, who.chatID, id, question)
	if err != nil {
		log.Printf("approval: could not ask chat %d: %v", who.chatID, err)
		return &sdk.ElicitResult{Action: "cancel"}, nil
	}

	var action, outcome string
	select {
	case action = <-p.decision:
		outcome = map[string]string{"accept": "✅ Aprovado", "decline": "❌ Recusado"}[action]
	case <-time.After(a.timeout):
		action, outcome = "cancel", "⌛ Expirou sem resposta"
	case <-ctx.Done():
		action, outcome = "cancel", "⌛ Cancelado"
	}

	log.Printf("approval %s for user %d: %s", id, who.userID, action)

	// WithoutCancel so the buttons are removed even if the request was cancelled.
	edited := question.html + "\n\n<b>" + outcome + "</b>"
	bg := context.WithoutCancel(ctx)
	if isPhoto {
		_, err = a.tg.EditMessageCaption(bg, &bot.EditMessageCaptionParams{
			ChatID: who.chatID, MessageID: msg.ID, Caption: edited, ParseMode: models.ParseModeHTML,
		})
	} else {
		_, err = a.tg.EditMessageText(bg, &bot.EditMessageTextParams{
			ChatID: who.chatID, MessageID: msg.ID, Text: edited, ParseMode: models.ParseModeHTML,
		})
	}
	if err != nil {
		log.Printf("approval %s: could not update the question: %v", id, err)
	}

	return &sdk.ElicitResult{Action: action}, nil
}

const outcomeRoom = 32

func (a *Approvals) ask(ctx context.Context, chatID int64, id string, q formattedApproval) (*models.Message, bool, error) {
	buttons := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ Aprovar", CallbackData: callbackPrefix + id + ":y"},
		{Text: "❌ Recusar", CallbackData: callbackPrefix + id + ":n"},
	}}}

	if q.poster != "" && utf16Len(plainText(parseHTML(q.html)))+outcomeRoom <= captionLimit {
		msg, err := a.tg.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileString{Data: q.poster},
			Caption:     q.html,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: buttons,
		})
		if err == nil {
			return msg, true, nil
		}
		log.Printf("approval %s: the cover could not be sent, asking in text: %v", id, err)
	}

	msg, err := a.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               q.html,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: new(true)},
		ReplyMarkup:        buttons,
	})
	return msg, false, err
}

// Answer accepts a press only from the user who raised the question, and only once.
func (a *Approvals) Answer(userID int64, data string) string {
	id, choice, ok := strings.Cut(strings.TrimPrefix(data, callbackPrefix), ":")
	if !ok || (choice != "y" && choice != "n") {
		return "Botão inválido."
	}

	a.mu.Lock()
	p, found := a.pending[id]
	if found && p.userID == userID {
		delete(a.pending, id)
	}
	a.mu.Unlock()

	switch {
	case !found:
		return "Essa aprovação já foi respondida ou expirou."
	case p.userID != userID:
		log.Printf("approval %s: user %d pressed a button on user %d's request", id, userID, p.userID)
		return "Só quem fez o pedido pode responder."
	}

	if choice == "y" {
		p.decision <- "accept"
		return "Aprovado."
	}
	p.decision <- "decline"
	return "Recusado."
}

// callback_data is capped at 64 bytes. Random, so an old button cannot match a new question.
func newApprovalID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("approval id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
