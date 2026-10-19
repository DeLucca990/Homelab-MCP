package telegram

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
)

// Commands call tools directly, without Claude.

type command struct {
	name        string
	description string
	tools       []string
	usage       string
	args        func(rest string) (map[string]any, error)
}

var toolCommands = []command{
	{name: "status", description: "O que precisa de atenção no servidor", tools: []string{"homelab_overview"}},
	{name: "disk", description: "Uso de disco e inodes", tools: []string{"system_disk_usage"}},
	{name: "memory", description: "Memória e swap", tools: []string{"system_memory_stats"}},
	{name: "docker", description: "Status dos containers", tools: []string{"docker_container_status"}},
	{name: "logs", description: "Últimas linhas do log de um container", tools: []string{"docker_container_logs"},
		usage: "<container> [linhas]", args: logsArgs},
	{name: "restart", description: "Reinicia um container (admins)", tools: []string{"docker_container_restart"},
		usage: "<container>", args: containerArgs},
	{name: "queue", description: "Filas de download do Radarr e do Sonarr", tools: []string{"radarr_queue_status", "sonarr_queue_status"}},
	{name: "missing", description: "Episódios que faltam no Sonarr", tools: []string{"sonarr_missing_episodes"}},
	{name: "sessions", description: "Quem está assistindo no Jellyfin", tools: []string{"jellyfin_active_sessions"}},
	{name: "search", description: "Procura um filme ou uma série no Radarr e no Sonarr", tools: []string{"radarr_movie_lookup", "sonarr_series_lookup"},
		usage: "<título>", args: termArgs},
}

var otherCommands = []command{
	{name: "digest", description: "Resumo das últimas 24h"},
	{name: "cost", description: "Quanto o Claude custou: hoje, 7 e 30 dias"},
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

func parseCommand(text string) (name, target, rest string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", ""
	}
	name = text[1:]
	if i := strings.IndexFunc(name, unicode.IsSpace); i >= 0 {
		name, rest = name[:i], strings.TrimSpace(name[i:])
	}
	name, target, _ = strings.Cut(name, "@")
	return strings.ToLower(name), target, rest
}

func (c command) usageLine() string {
	if c.usage == "" {
		return "/" + c.name
	}
	return "/" + c.name + " " + c.usage
}

func (c command) menuDescription() string {
	if c.usage == "" {
		return c.description
	}
	return c.description + " · " + c.usage
}

func logsArgs(rest string) (map[string]any, error) {
	fields := strings.Fields(rest)
	switch {
	case len(fields) == 0:
		return nil, errors.New("Diga de qual container.")
	case len(fields) > 2:
		return nil, errors.New("Um container por vez, e o número de linhas depois dele.")
	}
	args := map[string]any{"container": fields[0]}
	if len(fields) == 2 {
		lines, err := strconv.Atoi(fields[1])
		if err != nil || lines <= 0 {
			return nil, errors.New("O número de linhas tem que ser um inteiro positivo.")
		}
		args["tail"] = lines
	}
	return args, nil
}

func containerArgs(rest string) (map[string]any, error) {
	fields := strings.Fields(rest)
	switch {
	case len(fields) == 0:
		return nil, errors.New("Diga qual container.")
	case len(fields) > 1:
		return nil, errors.New("Um container por vez.")
	}
	return map[string]any{"container": fields[0]}, nil
}

func termArgs(rest string) (map[string]any, error) {
	if rest == "" {
		return nil, errors.New("Diga o que procurar.")
	}
	return map[string]any{"term": rest}, nil
}

func helpText(llm, admin bool) string {
	var b strings.Builder
	b.WriteString("<b>Homelab</b>\n\n")
	for _, c := range append(toolCommands, otherCommands...) {
		b.WriteString(escape(c.usageLine()) + " — " + escape(c.description) + "\n")
	}
	if llm {
		example, limit := "reinicia o jellyfin", ""
		if !admin {
			example, limit = "adiciona Duna ao Radarr", " Remover, reiniciar e mudar configurações fica com os administradores."
		}
		b.WriteString("\nOu escreva em linguagem natural: <i>por que o Dune não baixou?</i>, " +
			"<i>" + example + "</i>. Ações que mudam algo pedem sua aprovação antes." + limit + "\n\n" +
			"Em grupo, eu só respondo a comandos, a uma @menção ou a uma resposta a mensagem minha.")
	} else {
		b.WriteString("\nLinguagem natural está desligada: defina ANTHROPIC_API_KEY no servidor do bot para ligar.")
	}
	return b.String()
}
