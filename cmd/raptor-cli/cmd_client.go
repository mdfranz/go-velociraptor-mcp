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
	flagClientLabel    string
	flagClientOnline   bool
	flagClientDescribe string
	flagClientMetadata string
)

func init() {
	rootCmd.AddCommand(clientCmd)
	clientCmd.AddCommand(clientInfoCmd, clientListCmd, clientDescribeCmd, clientMetadataCmd)

	clientListCmd.Flags().StringVar(&flagClientSearch, "search", ".", "hostname/fqdn/client_id regex")
	clientListCmd.Flags().StringVar(&flagClientOSFilter, "os", ".", "OS type regex filter")
	clientListCmd.Flags().IntVar(&flagClientLimit, "limit", 50, "max results")
	clientListCmd.Flags().StringVar(&flagClientLabel, "label", "", "label to filter using the server index")
	clientListCmd.Flags().BoolVar(&flagClientOnline, "online", false, "only clients seen within the last 15 minutes")

	clientDescribeCmd.Flags().StringVar(&flagClientDescribe, "client", "", "client_id (required)")
	_ = clientDescribeCmd.MarkFlagRequired("client")

	clientMetadataCmd.Flags().StringVar(&flagClientMetadata, "client", "", "client_id (required)")
	_ = clientMetadataCmd.MarkFlagRequired("client")
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
		if flagClientLimit <= 0 || flagClientLimit > 1000 {
			return fmt.Errorf("limit must be between 1 and 1000")
		}
		onlineWhere := ""
		if flagClientOnline {
			onlineWhere = "\n  AND (now() - last_seen_at / 1000000) < 900"
		}
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
FROM %s
WHERE (os_info.hostname =~ %s OR os_info.fqdn =~ %s OR client_id =~ %s)
  AND os_info.system =~ %s%s
ORDER BY LastSeen DESC LIMIT %d`,
			clientSearchSource(),
			quote(flagClientSearch), quote(flagClientSearch), quote(flagClientSearch),
			quote(flagClientOSFilter), onlineWhere, flagClientLimit)
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var clientDescribeCmd = &cobra.Command{
	Use:   "describe",
	Short: "Describe a client by client ID",
	RunE: func(cmd *cobra.Command, args []string) error {
		vql := fmt.Sprintf(`SELECT * FROM clients(client_id=%s) LIMIT 1`,
			quote(flagClientDescribe))
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var clientMetadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Read client metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		vql := fmt.Sprintf(
			`SELECT client_metadata(client_id=%s) AS metadata FROM scope()`,
			quote(flagClientMetadata))
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

func clientSearchSource() string {
	if flagClientLabel == "" {
		return "clients()"
	}
	return fmt.Sprintf("clients(search=%s)", quote("label:"+flagClientLabel))
}

func quote(s string) string {
	return raptor.VQLLiteral(s)
}
