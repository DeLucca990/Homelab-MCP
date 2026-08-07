package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyInput struct{}

func registerTools(s *sdk.Server) {
	// system host tool
	sdk.AddTool(s, &sdk.Tool{
		Name:        "system_host_info",
		Description: "Retorna informações gerais do servidor: hostname, sistema operacional, versão do kernel, arquitetura e uptime.",
	}, handleHostInfo)

	// system cpu cores
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_cpu_cores",
		Description: "Retorna o uso detalhado de cada núcleo de CPU individualmente, " +
			"decomposto em tempo de usuário, kernel, nice, interrupções e espera de I/O — " +
			"equivalente às barras por núcleo do htop. Leva cerca de 500ms.",
	}, handleCoreUsage)

	// system memory tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_memory_stats",
		Description: "Retorna o uso de memória RAM e swap do servidor. " +
			"Para avaliar pressão de memória use 'available' e 'used_percent', " +
			"nunca 'free' — o Linux mantém a RAM ociosa ocupada com cache de disco, " +
			"então 'free' baixo é normal e não indica problema. Resposta imediata.",
	}, handleMemoryStats)

	// system disk tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_disk_usage",
		Description: "Retorna o uso de espaço em disco por ponto de montagem, ordenado do " +
			"mais cheio para o mais vazio. Por padrão filtra pseudo-filesystems, pacotes snap " +
			"e camadas de container, que aparecem como 100% cheios sem que isso indique problema. " +
			"Inclui também o uso de inodes: um disco pode ficar inutilizável por esgotamento de " +
			"inodes mesmo com bytes livres de sobra.",
	}, handleDiskStats)
}
