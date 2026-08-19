package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/commissionapi"
	"github.com/spf13/cobra"
)

var commissionCmd = &cobra.Command{
	Use:   "commission",
	Short: "Manage commissions and discover available work",
}

func commissionInput(cmd *cobra.Command) (commissionapi.CommissionInput, error) {
	input := commissionapi.CommissionInput{}
	input.Title, _ = cmd.Flags().GetString("title")
	input.CapabilityDescription, _ = cmd.Flags().GetString("capability-description")
	input.RequestSpecText, _ = cmd.Flags().GetString("request-spec-text")
	input.DeliverySpecText, _ = cmd.Flags().GetString("delivery-spec-text")
	tags, _ := cmd.Flags().GetString("tags")
	input.Tags = commaList(tags)
	input.PriceFen, _ = cmd.Flags().GetInt64("price-fen")
	input.Currency, _ = cmd.Flags().GetString("currency")
	input.PromisedDeliveryMS, _ = cmd.Flags().GetInt64("promised-delivery-ms")
	input.RequestSpecSchema, _ = cmd.Flags().GetString("request-spec-schema")
	input.DeliverySpecSchema, _ = cmd.Flags().GetString("delivery-spec-schema")
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.CapabilityDescription) == "" ||
		strings.TrimSpace(input.RequestSpecText) == "" || strings.TrimSpace(input.DeliverySpecText) == "" ||
		len(input.Tags) == 0 || input.PriceFen < 0 || input.PromisedDeliveryMS <= 0 ||
		strings.TrimSpace(input.Currency) == "" || strings.TrimSpace(input.RequestSpecSchema) == "" ||
		strings.TrimSpace(input.DeliverySpecSchema) == "" {
		return input, fmt.Errorf("title, capability description, request/delivery specs, tags, non-negative price, currency, positive promised delivery, and request/delivery schemas are required")
	}
	return input, nil
}

func addCommissionInputFlags(command *cobra.Command) {
	command.Flags().String("title", "", "commission title")
	command.Flags().String("capability-description", "", "capability offered by this commission")
	command.Flags().String("request-spec-text", "", "human-readable buyer input specification")
	command.Flags().String("delivery-spec-text", "", "human-readable delivery specification")
	command.Flags().String("tags", "", "comma-separated discovery tags")
	command.Flags().Int64("price-fen", -1, "price in minor currency units")
	command.Flags().String("currency", "CNY", "ISO currency code")
	command.Flags().Int64("promised-delivery-ms", 0, "promised delivery duration in milliseconds")
	command.Flags().String("request-spec-schema", "{}", "request JSON Schema")
	command.Flags().String("delivery-spec-schema", "{}", "delivery JSON Schema")
}

func addIdempotencyFlag(command *cobra.Command) {
	command.Flags().String("idempotency-key", "", "explicit idempotency key (stable key generated when omitted)")
}

func numericArgument(args []string, kind string) (int64, error) {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", kind)
	}
	return id, nil
}

var commissionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a draft commission",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		input, err := commissionInput(cmd)
		if err != nil {
			return err
		}
		key, _ := cmd.Flags().GetString("idempotency-key")
		resp, err := postMutation(newCommissionClient(), "/commissions", "commission.create", key, map[string]any{"input": input})
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var commissionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List commissions owned by the authenticated agent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cursor, _ := cmd.Flags().GetInt64("cursor")
		limit, _ := cmd.Flags().GetInt("limit")
		params := map[string]string{"limit": strconv.Itoa(limit)}
		if cursor > 0 {
			params["cursor"] = strconv.FormatInt(cursor, 10)
		}
		resp, err := newCommissionClient().Get("/commissions", params)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var commissionGetCmd = &cobra.Command{
	Use:   "get <commission-id>",
	Short: "Get an owned commission",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id, err := numericArgument(args, "commission ID")
		if err != nil {
			return err
		}
		resp, err := newCommissionClient().Get("/commissions/"+strconv.FormatInt(id, 10), nil)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var commissionUpdateCmd = &cobra.Command{
	Use:   "update <commission-id>",
	Short: "Update a commission draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := numericArgument(args, "commission ID")
		if err != nil {
			return err
		}
		input, err := commissionInput(cmd)
		if err != nil {
			return err
		}
		version, _ := cmd.Flags().GetInt64("expected-version")
		if version <= 0 {
			return fmt.Errorf("--expected-version must be positive")
		}
		body := map[string]any{"expected_draft_version": version, "input": input}
		key, _ := cmd.Flags().GetString("idempotency-key")
		path := "/commissions/" + strconv.FormatInt(id, 10) + "/draft"
		resp, err := putMutation(newCommissionClient(), path, "commission.update."+strconv.FormatInt(id, 10), key, body)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

func commissionVersionMutation(operation, suffix string) *cobra.Command {
	command := &cobra.Command{
		Use:   operation + " <commission-id>",
		Short: strings.ToUpper(operation[:1]) + operation[1:] + " a commission",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := numericArgument(args, "commission ID")
			if err != nil {
				return err
			}
			version, _ := cmd.Flags().GetInt64("expected-version")
			if version <= 0 {
				return fmt.Errorf("--expected-version must be positive")
			}
			body := map[string]any{"expected_draft_version": version}
			key, _ := cmd.Flags().GetString("idempotency-key")
			path := "/commissions/" + strconv.FormatInt(id, 10) + suffix
			resp, err := postMutation(newCommissionClient(), path, "commission."+operation+"."+strconv.FormatInt(id, 10), key, body)
			if err != nil {
				return err
			}
			return printResponse(resp)
		},
	}
	command.Flags().Int64("expected-version", 0, "expected draft version")
	addIdempotencyFlag(command)
	return command
}

var commissionPublishCmd = commissionVersionMutation("publish", "/publish")

var commissionOfflineCmd = &cobra.Command{
	Use:   "offline <commission-id>",
	Short: "Take a commission offline",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := numericArgument(args, "commission ID")
		if err != nil {
			return err
		}
		key, _ := cmd.Flags().GetString("idempotency-key")
		path := "/commissions/" + strconv.FormatInt(id, 10) + "/offline"
		resp, err := postMutation(newCommissionClient(), path, "commission.offline."+strconv.FormatInt(id, 10), key, nil)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

func discoveryParams(cmd *cobra.Command, includeQuery bool) (map[string]string, error) {
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("--limit must be between 1 and 100")
	}
	params := map[string]string{"limit": strconv.Itoa(limit)}
	if includeQuery {
		query, _ := cmd.Flags().GetString("query")
		query = strings.TrimSpace(query)
		if query == "" {
			return nil, fmt.Errorf("--query is required")
		}
		params["query"] = query
	}
	for _, name := range []string{"min-price-fen", "max-price-fen", "min-promised-delivery-ms", "max-promised-delivery-ms"} {
		value, _ := cmd.Flags().GetInt64(name)
		if value >= 0 {
			params[strings.ReplaceAll(name, "-", "_")] = strconv.FormatInt(value, 10)
		}
	}
	return params, nil
}

func addDiscoveryFlags(command *cobra.Command, query bool) {
	if query {
		command.Flags().String("query", "", "search query")
	}
	command.Flags().Int("limit", 20, "maximum candidates (1-100)")
	command.Flags().Int64("min-price-fen", -1, "minimum price in minor currency units")
	command.Flags().Int64("max-price-fen", -1, "maximum price in minor currency units")
	command.Flags().Int64("min-promised-delivery-ms", -1, "minimum promised delivery duration")
	command.Flags().Int64("max-promised-delivery-ms", -1, "maximum promised delivery duration")
}

func discoveryCommand(use, short, path string, query bool) *cobra.Command {
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			params, err := discoveryParams(cmd, query)
			if err != nil {
				return err
			}
			resp, err := newClient().Get(path, params)
			if err != nil {
				return err
			}
			return printResponse(resp)
		},
	}
	addDiscoveryFlags(command, query)
	return command
}

var commissionSearchCmd = discoveryCommand("search", "Search published commissions", "/commissions/search", true)
var commissionRecommendCmd = discoveryCommand("recommend", "Recommend commissions for the authenticated agent", "/commissions/recommendations", false)

func commissionReadCommand(use, short, suffix string, flags func(*cobra.Command)) *cobra.Command {
	command := &cobra.Command{
		Use:   use + " <commission-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := numericArgument(args, "commission ID")
			if err != nil {
				return err
			}
			params := map[string]string{}
			if cmd.Flags().Lookup("limit") != nil {
				limit, _ := cmd.Flags().GetInt("limit")
				params["limit"] = strconv.Itoa(limit)
				if cursor, _ := cmd.Flags().GetString("cursor"); cursor != "" {
					params["cursor"] = cursor
				}
			}
			resp, err := newCommissionClient().Get("/commissions/"+strconv.FormatInt(id, 10)+suffix, params)
			if err != nil {
				return err
			}
			return printResponse(resp)
		},
	}
	if flags != nil {
		flags(command)
	}
	return command
}

var commissionReviewsCmd = commissionReadCommand("reviews", "List commission reviews", "/reviews", func(command *cobra.Command) {
	command.Flags().Int("limit", 20, "maximum reviews")
	command.Flags().String("cursor", "", "pagination cursor")
})
var commissionStatisticsCmd = commissionReadCommand("statistics", "Get commission statistics", "/statistics", nil)

func init() {
	addCommissionInputFlags(commissionCreateCmd)
	addIdempotencyFlag(commissionCreateCmd)
	commissionListCmd.Flags().Int64("cursor", 0, "pagination cursor")
	commissionListCmd.Flags().Int("limit", 20, "maximum commissions")
	addCommissionInputFlags(commissionUpdateCmd)
	commissionUpdateCmd.Flags().Int64("expected-version", 0, "expected draft version")
	addIdempotencyFlag(commissionUpdateCmd)
	addIdempotencyFlag(commissionOfflineCmd)
	commissionCmd.AddCommand(commissionCreateCmd, commissionListCmd, commissionGetCmd, commissionUpdateCmd,
		commissionPublishCmd, commissionOfflineCmd, commissionSearchCmd, commissionRecommendCmd,
		commissionReviewsCmd, commissionStatisticsCmd)
	rootCmd.AddCommand(commissionCmd)
}
