package main

import (
	"fmt"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagClientSearch   string
	flagClientOSFilter string
	flagClientLimit    int
)

func init() {
	rootCmd.AddCommand(clientCmd)
	clientCmd.AddCommand(clientInfoCmd, clientListCmd)

	clientListCmd.Flags().StringVar(&flagClientSearch, "search", ".", "hostname/fqdn/client_id regex")
	clientListCmd.Flags().StringVar(&flagClientOSFilter, "os", ".", "OS type regex filter")
	clientListCmd.Flags().IntVar(&flagClientLimit, "limit", 50, "max results")
}

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Client discovery",
}

var clientInfoCmd = &cobra.Command{
	Use:   "info <hostname>",
	Short: "Get best matching client by hostname or FQDN",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		vql := fmt.Sprintf(`
SELECT client_id,
       timestamp(epoch=first_seen_at) as FirstSeen,
       timestamp(epoch=last_seen_at) as LastSeen,
       os_info.hostname as Hostname,
       os_info.fqdn as Fqdn,
       os_info.system as OSType,
       os_info.release as OS,
       os_info.machine as Machine,
       agent_information.version as AgentVersion
FROM clients()
WHERE os_info.hostname =~ %s OR os_info.fqdn =~ %s
ORDER BY LastSeen DESC LIMIT 1`, quote(hostname), quote(hostname))
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var clientListCmd = &cobra.Command{
	Use:   "list",
	Short: "List clients",
	RunE: func(cmd *cobra.Command, args []string) error {
		vql := fmt.Sprintf(`
SELECT client_id,
       timestamp(epoch=first_seen_at) as FirstSeen,
       timestamp(epoch=last_seen_at) as LastSeen,
       os_info.hostname as Hostname,
       os_info.fqdn as Fqdn,
       os_info.system as OSType,
       os_info.release as OS,
       os_info.machine as Machine,
       agent_information.version as AgentVersion,
       last_ip as LastIP
FROM clients()
WHERE (os_info.hostname =~ %s OR os_info.fqdn =~ %s OR client_id =~ %s)
  AND os_info.system =~ %s
ORDER BY LastSeen DESC LIMIT %d`,
			quote(flagClientSearch), quote(flagClientSearch), quote(flagClientSearch),
			quote(flagClientOSFilter), flagClientLimit)
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

func quote(s string) string {
	return raptor.VQLLiteral(s)
}
