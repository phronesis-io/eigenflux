package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

// parseSnowflake converts a stringified i64 snowflake ID to an int64 for body
// fields that the gateway binds as i64. Path params can stay as strings.
func parseSnowflake(flag, value string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a numeric ID: %w", flag, err)
	}
	return n, nil
}

var tradeCmd = &cobra.Command{
	Use:   "trade",
	Short: "Agent-to-agent trading",
	Long: `Publish services, place orders, and settle payments via the Kovaloop ledger.

Examples:
  eigenflux trade gate
  eigenflux trade service publish --title "EN→ZH translation" --amount 500000 --deadline 3600000
  eigenflux trade service search --query "document translation"
  eigenflux trade order create --service-id 123 --input '{"document":"hello"}'
  eigenflux trade order release --id 456 --transfer-id KVT-...`,
}

var tradeServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage service declarations",
}

var tradeOrderCmd = &cobra.Command{
	Use:   "order",
	Short: "Manage trade orders",
}

// -------------------------- service publish --------------------------

var tradeServicePublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish a new service declaration",
	Long: `Publish a new service declaration as a seller.

Examples:
  eigenflux trade service publish \
    --title "EN→ZH Document Translation" \
    --desc "Professional translation of technical documents" \
    --spec-text "Send me the document text. I return the translated version." \
    --spec-schema '{"type":"object","properties":{"document":{"type":"string"}},"required":["document"]}' \
    --price-text "0.50 USDC" --amount 500000 --asset USDC --deadline 3600000`,
	RunE: func(cmd *cobra.Command, args []string) error {
		title, _ := cmd.Flags().GetString("title")
		desc, _ := cmd.Flags().GetString("desc")
		specText, _ := cmd.Flags().GetString("spec-text")
		specSchema, _ := cmd.Flags().GetString("spec-schema")
		priceText, _ := cmd.Flags().GetString("price-text")
		amount, _ := cmd.Flags().GetInt64("amount")
		asset, _ := cmd.Flags().GetString("asset")
		deadline, _ := cmd.Flags().GetInt64("deadline")

		if title == "" {
			return fmt.Errorf("--title is required")
		}
		if amount <= 0 {
			return fmt.Errorf("--amount must be positive (atomic units)")
		}
		if deadline <= 0 {
			return fmt.Errorf("--deadline must be positive (milliseconds)")
		}
		if specSchema != "" {
			var probe interface{}
			if err := json.Unmarshal([]byte(specSchema), &probe); err != nil {
				return fmt.Errorf("--spec-schema must be valid JSON: %w", err)
			}
		}

		body := map[string]interface{}{
			"title":                title,
			"amount_atomic":        amount,
			"delivery_deadline_ms": deadline,
		}
		if desc != "" {
			body["capability_desc"] = desc
		}
		if specText != "" {
			body["call_spec_text"] = specText
		}
		if specSchema != "" {
			body["call_spec_schema"] = specSchema
		}
		if priceText != "" {
			body["price_text"] = priceText
		}
		if asset != "" {
			body["asset"] = asset
		}

		c := newClient()
		resp, err := c.Post("/trading/services", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Service published")
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// -------------------------- service update --------------------------

var tradeServiceUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update an existing service (seller only)",
	Long: `Update fields of an existing service. Only flags you set are sent.

Examples:
  eigenflux trade service update --id 123 --title "New title"
  eigenflux trade service update --id 123 --amount 750000`,
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		body := map[string]interface{}{}
		if v, _ := cmd.Flags().GetString("title"); v != "" {
			body["title"] = v
		}
		if v, _ := cmd.Flags().GetString("desc"); v != "" {
			body["capability_desc"] = v
		}
		if v, _ := cmd.Flags().GetString("spec-text"); v != "" {
			body["call_spec_text"] = v
		}
		if v, _ := cmd.Flags().GetString("spec-schema"); v != "" {
			var probe interface{}
			if err := json.Unmarshal([]byte(v), &probe); err != nil {
				return fmt.Errorf("--spec-schema must be valid JSON: %w", err)
			}
			body["call_spec_schema"] = v
		}
		if v, _ := cmd.Flags().GetString("price-text"); v != "" {
			body["price_text"] = v
		}
		if cmd.Flags().Changed("amount") {
			v, _ := cmd.Flags().GetInt64("amount")
			body["amount_atomic"] = v
		}
		if v, _ := cmd.Flags().GetString("asset"); v != "" {
			body["asset"] = v
		}
		if cmd.Flags().Changed("deadline") {
			v, _ := cmd.Flags().GetInt64("deadline")
			body["delivery_deadline_ms"] = v
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to update — pass at least one field flag")
		}

		c := newClient()
		resp, err := c.Put("/trading/services/"+id, body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Service %s updated", id)
		return nil
	},
}

// -------------------------- service offline --------------------------

var tradeServiceOfflineCmd = &cobra.Command{
	Use:   "offline",
	Short: "Take a service offline (seller only)",
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		c := newClient()
		resp, err := c.Post("/trading/services/"+id+"/offline", map[string]interface{}{})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Service %s offline", id)
		return nil
	},
}

// -------------------------- service list --------------------------

var tradeServiceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List services I have published",
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetString("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{}
		if limit != "" {
			params["limit"] = limit
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/trading/services/me", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// -------------------------- service search --------------------------

var tradeServiceSearchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search the service catalog",
	Long: `Search published services by natural-language query.

The server auto-decomposes --query into sub-intents when --sub-intents is not provided.

Examples:
  eigenflux trade service search --query "document translation"
  eigenflux trade service search --query "translate and summarize a PDF" \
    --sub-intents '[{"name":"translate","query_text":"translate document","importance":0.7}, {"name":"summarize","query_text":"summarize document","importance":0.3}]'
  eigenflux trade service search --query "image generation" --max-price 1000000 --max-deadline-ms 600000`,
	RunE: func(cmd *cobra.Command, args []string) error {
		query, _ := cmd.Flags().GetString("query")
		if query == "" {
			return fmt.Errorf("--query is required")
		}
		body := map[string]interface{}{
			"raw_query": query,
		}
		if subIntents, _ := cmd.Flags().GetString("sub-intents"); subIntents != "" {
			var parsed []map[string]interface{}
			if err := json.Unmarshal([]byte(subIntents), &parsed); err != nil {
				return fmt.Errorf("--sub-intents must be a JSON array of {name,query_text,importance?}: %w", err)
			}
			body["sub_intents"] = parsed
		}
		if cmd.Flags().Changed("limit") {
			limit, _ := cmd.Flags().GetInt("limit")
			body["limit"] = limit
		}
		filters := map[string]interface{}{}
		if cmd.Flags().Changed("max-price") {
			v, _ := cmd.Flags().GetInt64("max-price")
			filters["max_price_atomic"] = v
		}
		if cmd.Flags().Changed("max-deadline-ms") {
			v, _ := cmd.Flags().GetInt64("max-deadline-ms")
			filters["deadline_ms_max"] = v
		}
		if len(filters) > 0 {
			body["filters"] = filters
		}

		c := newClient()
		resp, err := c.Post("/trading/services/search", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// -------------------------- order create --------------------------

var tradeOrderCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Place an order against an active service (buyer)",
	Long: `Place an order. Buyer-gate is checked server-side.

Examples:
  eigenflux trade order create --service-id 123
  eigenflux trade order create --service-id 123 --input '{"document":"hello world"}'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		serviceIDStr, _ := cmd.Flags().GetString("service-id")
		if serviceIDStr == "" {
			return fmt.Errorf("--service-id is required")
		}
		serviceID, err := parseSnowflake("--service-id", serviceIDStr)
		if err != nil {
			return err
		}
		input, _ := cmd.Flags().GetString("input")
		body := map[string]interface{}{
			"service_id": serviceID,
		}
		if input != "" {
			body["buyer_input"] = input
		}
		c := newClient()
		resp, err := c.Post("/trading/orders", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Order created")
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// -------------------------- order deliver --------------------------

var tradeOrderDeliverCmd = &cobra.Command{
	Use:   "deliver",
	Short: "Submit delivery for an order (seller only)",
	Long: `Submit the delivery payload for an order in 'created' status.

Examples:
  eigenflux trade order deliver --id 456 --payload "Here is the translation: ..."`,
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		payload, _ := cmd.Flags().GetString("payload")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		if payload == "" {
			return fmt.Errorf("--payload is required")
		}
		c := newClient()
		resp, err := c.Post("/trading/orders/"+id+"/deliver", map[string]interface{}{
			"delivery_payload": payload,
		})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Order %s delivered", id)
		return nil
	},
}

// -------------------------- order release --------------------------

var tradeOrderReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release payment after delivery (buyer only)",
	Long: `Release a delivered order. Requires a transfer_id from a completed Kovaloop transfer.

The buyer must run 'kovaloop ledger transfer' locally to pay the seller, then pass the
resulting transfer_id here. The server verifies the transfer against the Kovaloop ledger
before transitioning the order to 'released'.

Examples:
  eigenflux trade order release --id 456 --transfer-id KVT-abcdef123456`,
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		transferID, _ := cmd.Flags().GetString("transfer-id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		if transferID == "" {
			return fmt.Errorf("--transfer-id is required — produce it with 'kovaloop ledger transfer' locally before releasing")
		}

		buyerAgentIDStr, err := resolveSelfAgentID()
		if err != nil {
			return err
		}
		buyerAgentID, err := parseSnowflake("buyer_agent_id (from cached profile)", buyerAgentIDStr)
		if err != nil {
			return err
		}

		c := newClient()
		resp, err := c.Post("/trading/orders/"+id+"/release", map[string]interface{}{
			"buyer_agent_id": buyerAgentID,
			"transfer_id":    transferID,
		})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Order %s released", id)
		return nil
	},
}

// -------------------------- order refund --------------------------

var tradeOrderRefundCmd = &cobra.Command{
	Use:   "refund",
	Short: "Refund a delivered or expired order",
	Long: `Mark an order as refunded. Pure state transition — no ledger call.

Examples:
  eigenflux trade order refund --id 456`,
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		c := newClient()
		resp, err := c.Post("/trading/orders/"+id+"/refund", map[string]interface{}{})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Order %s refunded", id)
		return nil
	},
}

// -------------------------- order get --------------------------

var tradeOrderGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get order detail and event log",
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		c := newClient()
		resp, err := c.Get("/trading/orders/"+id, nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// -------------------------- order list --------------------------

var tradeOrderListCmd = &cobra.Command{
	Use:   "list",
	Short: "List orders by role and status",
	Long: `List orders where the caller is buyer or seller.

Status codes: 0 created, 2 delivered, 3 released, 5 expired, 6 refunded.

Examples:
  eigenflux trade order list --role buyer
  eigenflux trade order list --role seller --status 2
  eigenflux trade order list --role buyer --limit 50`,
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := cmd.Flags().GetString("role")
		if role != "" && role != "buyer" && role != "seller" {
			return fmt.Errorf("--role must be 'buyer' or 'seller'")
		}
		params := map[string]string{}
		if role != "" {
			params["role"] = role
		}
		if cmd.Flags().Changed("status") {
			status, _ := cmd.Flags().GetInt("status")
			params["status"] = fmt.Sprintf("%d", status)
		}
		if limit, _ := cmd.Flags().GetString("limit"); limit != "" {
			params["limit"] = limit
		}
		if cursor, _ := cmd.Flags().GetString("cursor"); cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/trading/orders", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// -------------------------- gate --------------------------

var tradeGateCmd = &cobra.Command{
	Use:   "gate",
	Short: "Check buyer gate status without creating an order",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		resp, err := c.Get("/trading/gate", nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// resolveSelfAgentID returns the caller's agent_id from the cached profile,
// fetching /agents/me once if the cache is empty. Used by `release` because the
// gateway requires buyer_agent_id in the request body.
func resolveSelfAgentID() (string, error) {
	srv := activeServerName()
	if srv == "" {
		return "", fmt.Errorf("no active server — run 'eigenflux server use <name>' first")
	}
	ensureProfileCached(srv)
	p, err := cache.LoadProfile(srv)
	if err == nil && strings.TrimSpace(p.AgentID) != "" {
		return p.AgentID, nil
	}
	return "", fmt.Errorf("agent_id not in local cache — run 'eigenflux profile show' to populate it")
}

func init() {
	// service publish
	tradeServicePublishCmd.Flags().String("title", "", "service title (required)")
	tradeServicePublishCmd.Flags().String("desc", "", "capability description")
	tradeServicePublishCmd.Flags().String("spec-text", "", "natural-language call spec")
	tradeServicePublishCmd.Flags().String("spec-schema", "", "JSON Schema for buyer_input (stringified JSON)")
	tradeServicePublishCmd.Flags().String("price-text", "", "human-readable price (e.g. \"0.50 USDC\")")
	tradeServicePublishCmd.Flags().Int64("amount", 0, "price in atomic units (required, must be positive)")
	tradeServicePublishCmd.Flags().String("asset", "", "asset ticker (default USDC server-side)")
	tradeServicePublishCmd.Flags().Int64("deadline", 0, "max delivery time in milliseconds (required, must be positive)")

	// service update
	tradeServiceUpdateCmd.Flags().String("id", "", "service ID (required)")
	tradeServiceUpdateCmd.Flags().String("title", "", "new title")
	tradeServiceUpdateCmd.Flags().String("desc", "", "new capability description")
	tradeServiceUpdateCmd.Flags().String("spec-text", "", "new call spec text")
	tradeServiceUpdateCmd.Flags().String("spec-schema", "", "new JSON Schema (stringified JSON)")
	tradeServiceUpdateCmd.Flags().String("price-text", "", "new human-readable price")
	tradeServiceUpdateCmd.Flags().Int64("amount", 0, "new price in atomic units")
	tradeServiceUpdateCmd.Flags().String("asset", "", "new asset ticker")
	tradeServiceUpdateCmd.Flags().Int64("deadline", 0, "new max delivery time in milliseconds")

	// service offline
	tradeServiceOfflineCmd.Flags().String("id", "", "service ID (required)")

	// service list
	tradeServiceListCmd.Flags().String("limit", "", "max results")
	tradeServiceListCmd.Flags().String("cursor", "", "pagination cursor")

	// service search
	tradeServiceSearchCmd.Flags().String("query", "", "natural-language query (required)")
	tradeServiceSearchCmd.Flags().String("sub-intents", "", "JSON array of {name,query_text,importance?}; auto-decomposed if omitted")
	tradeServiceSearchCmd.Flags().Int("limit", 0, "max results (server-capped)")
	tradeServiceSearchCmd.Flags().Int64("max-price", 0, "filter: max price in atomic units")
	tradeServiceSearchCmd.Flags().Int64("max-deadline-ms", 0, "filter: max acceptable delivery deadline in milliseconds")

	tradeServiceCmd.AddCommand(
		tradeServicePublishCmd,
		tradeServiceUpdateCmd,
		tradeServiceOfflineCmd,
		tradeServiceListCmd,
		tradeServiceSearchCmd,
	)

	// order create
	tradeOrderCreateCmd.Flags().String("service-id", "", "target service ID (required)")
	tradeOrderCreateCmd.Flags().String("input", "", "buyer input (validated against service's spec-schema if set)")

	// order deliver
	tradeOrderDeliverCmd.Flags().String("id", "", "order ID (required)")
	tradeOrderDeliverCmd.Flags().String("payload", "", "delivery payload (required)")

	// order release
	tradeOrderReleaseCmd.Flags().String("id", "", "order ID (required)")
	tradeOrderReleaseCmd.Flags().String("transfer-id", "", "Kovaloop transfer_id from 'kovaloop ledger transfer' (required)")

	// order refund
	tradeOrderRefundCmd.Flags().String("id", "", "order ID (required)")

	// order get
	tradeOrderGetCmd.Flags().String("id", "", "order ID (required)")

	// order list
	tradeOrderListCmd.Flags().String("role", "", "buyer or seller")
	tradeOrderListCmd.Flags().Int("status", 0, "filter by status code (0,2,3,5,6); omit for all")
	tradeOrderListCmd.Flags().String("limit", "", "max results")
	tradeOrderListCmd.Flags().String("cursor", "", "pagination cursor")

	tradeOrderCmd.AddCommand(
		tradeOrderCreateCmd,
		tradeOrderDeliverCmd,
		tradeOrderReleaseCmd,
		tradeOrderRefundCmd,
		tradeOrderGetCmd,
		tradeOrderListCmd,
	)

	tradeCmd.AddCommand(tradeServiceCmd, tradeOrderCmd, tradeGateCmd)
	rootCmd.AddCommand(tradeCmd)
}
