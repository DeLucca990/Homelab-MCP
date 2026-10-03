package telegram

import (
	"strings"
)

// Commands call tools directly, without Claude.

type command struct {
	name        string
	description string
	tools       []string
}

var toolCommands = []command{
	{"status", "O que precisa de atenção no servidor", []string{"homelab_overview"}},
	{"disk", "Uso de disco e inodes", []string{"system_disk_usage"}},
	{"memory", "Memória e swap", []string{"system_memory_stats"}},
	{"docker", "Status dos containers", []string{"docker_container_status"}},
	{"queue", "Filas de download do Radarr e do Sonarr", []string{"radarr_queue_status", "sonarr_queue_status"}},
}

var otherCommands = []command{
	{name: "reset", description: "Começar uma conversa nova"},
	{name: "help", description: "O que este bot sabe fazer"},
}

func findCommand(name string) (command, bool) {
	for _, c := range toolCommands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

func parseCommand(text string) (name, target string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	name, _, _ = strings.Cut(text[1:], " ")
	name, target, _ = strings.Cut(name, "@")
	return strings.ToLower(name), target
}

func helpText(llm bool) string {
	var b strings.Builder
	b.WriteString("<b>Homelab</b>\n\n")
	for _, c := range append(toolCommands, otherCommands...) {
		b.WriteString("/" + c.name + " — " + escape(c.description) + "\n")
	}
	if llm {
		b.WriteString("\nOu escreva em linguagem natural: <i>por que o Dune não baixou?</i>, " +
			"<i>reinicia o jellyfin</i>. Ações que mudam algo pedem sua aprovação antes.\n\n" +
			"Em grupo, eu só respondo a comandos, a uma @menção ou a uma resposta a mensagem minha.")
	} else {
		b.WriteString("\nLinguagem natural está desligada: defina ANTHROPIC_API_KEY no servidor do bot para ligar.")
	}
	return b.String()
}
