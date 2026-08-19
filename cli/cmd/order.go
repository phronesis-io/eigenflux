package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var orderCmd = &cobra.Command{
	Use:   "order",
	Short: "Manage commission orders and workspace files",
}

var orderCreateCmd = &cobra.Command{
	Use:   "create <commission-id>",
	Short: "Create an order from a published commission",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		commissionID, err := numericArgument(args, "commission ID")
		if err != nil {
			return err
		}
		body := map[string]any{"commission_id": commissionID}
		if value, _ := cmd.Flags().GetString("buyer-input"); value != "" {
			body["buyer_input"] = value
		}
		if value, _ := cmd.Flags().GetString("impression-id"); value != "" {
			body["impression_id"] = value
		}
		key, _ := cmd.Flags().GetString("idempotency-key")
		resp, err := postMutation(newCommissionClient(), "/orders", "order.create", key, body)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var orderListCmd = &cobra.Command{
	Use:   "list",
	Short: "List orders for the authenticated agent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		params := map[string]string{}
		for _, name := range []string{"role", "state", "cursor"} {
			if value, _ := cmd.Flags().GetString(name); value != "" {
				params[name] = value
			}
		}
		limit, _ := cmd.Flags().GetInt("limit")
		params["limit"] = strconv.Itoa(limit)
		resp, err := newCommissionClient().Get("/orders", params)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

func orderReadCommand(use, short, suffix string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <order-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := numericArgument(args, "order ID")
			if err != nil {
				return err
			}
			resp, err := newCommissionClient().Get("/orders/"+strconv.FormatInt(id, 10)+suffix, nil)
			if err != nil {
				return err
			}
			return printResponse(resp)
		},
	}
}

var orderGetCmd = orderReadCommand("get", "Get an order and its event history", "")
var orderGetReviewCmd = orderReadCommand("get-review", "Get an order review", "/review")

func orderLifecycleCommand(operation string) *cobra.Command {
	short := strings.ToUpper(operation[:1]) + operation[1:] + " an order"
	if operation == "submit-materials" {
		short = "Submit order materials"
	}
	command := &cobra.Command{
		Use:   operation + " <order-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := numericArgument(args, "order ID")
			if err != nil {
				return err
			}
			version, _ := cmd.Flags().GetInt64("expected-version")
			if version <= 0 {
				return fmt.Errorf("--expected-version must be positive")
			}
			body := map[string]any{"expected_version": version}
			if reason, _ := cmd.Flags().GetString("reason"); reason != "" {
				body["reason"] = reason
			}
			key, _ := cmd.Flags().GetString("idempotency-key")
			path := "/orders/" + strconv.FormatInt(id, 10) + "/" + operation
			resp, err := postMutation(newCommissionClient(), path, "order."+operation+"."+strconv.FormatInt(id, 10), key, body)
			if err != nil {
				return err
			}
			return printResponse(resp)
		},
	}
	command.Flags().Int64("expected-version", 0, "expected order version")
	command.Flags().String("reason", "", "optional lifecycle reason")
	addIdempotencyFlag(command)
	return command
}

var orderSubmitMaterialsCmd = orderLifecycleCommand("submit-materials")
var orderCancelCmd = orderLifecycleCommand("cancel")
var orderAcceptCmd = orderLifecycleCommand("accept")
var orderRejectCmd = orderLifecycleCommand("reject")
var orderDeliverCmd = orderLifecycleCommand("deliver")
var orderCompleteCmd = orderLifecycleCommand("complete")

var orderReviewCmd = &cobra.Command{
	Use:   "review <order-id>",
	Short: "Review a completed order",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := numericArgument(args, "order ID")
		if err != nil {
			return err
		}
		score, _ := cmd.Flags().GetInt("score")
		if score < 1 || score > 5 {
			return fmt.Errorf("--score must be between 1 and 5")
		}
		body := map[string]any{"score": score}
		if value, _ := cmd.Flags().GetString("text"); value != "" {
			body["text"] = value
		}
		key, _ := cmd.Flags().GetString("idempotency-key")
		path := "/orders/" + strconv.FormatInt(id, 10) + "/review"
		resp, err := postMutation(newCommissionClient(), path, "order.review."+strconv.FormatInt(id, 10), key, body)
		if err != nil {
			return err
		}
		return printResponse(resp)
	},
}

var orderUploadCmd = &cobra.Command{
	Use:   "upload <order-id>",
	Short: "Upload a workspace file directly through a presigned grant",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := numericArgument(args, "order ID")
		if err != nil {
			return err
		}
		localPath, _ := cmd.Flags().GetString("file")
		if strings.TrimSpace(localPath) == "" {
			return fmt.Errorf("--file is required")
		}
		logicalPath, _ := cmd.Flags().GetString("path")
		if logicalPath == "" {
			logicalPath = filepath.Base(localPath)
		}
		size, digest, err := commissionapi.FileDigest(localPath)
		if err != nil {
			return fmt.Errorf("inspect upload file: %w", err)
		}
		beginBody := map[string]any{"logical_path": logicalPath, "byte_size": size, "sha256": digest}
		key, _ := cmd.Flags().GetString("idempotency-key")
		client := newCommissionClient()
		basePath := "/orders/" + strconv.FormatInt(id, 10) + "/uploads"
		response, err := postMutation(client, basePath, "order.upload.begin."+strconv.FormatInt(id, 10), key, beginBody)
		if err != nil {
			return err
		}
		if response.Code != 0 {
			return fmt.Errorf("%s", response.Msg)
		}
		var data commissionapi.TransferGrantData
		if err := json.Unmarshal(response.Data, &data); err != nil {
			return fmt.Errorf("parse upload grant: %w", err)
		}
		if data.Grant.URL == "" || data.Grant.ObjectID <= 0 {
			return fmt.Errorf("upload grant is incomplete")
		}
		if err := commissionapi.Upload(context.Background(), client.HTTPClient, data.Grant, localPath); err != nil {
			return err
		}
		confirmBody := map[string]any{"object_id": data.Grant.ObjectID}
		confirm, err := postMutation(client, basePath+"/confirm", "order.upload.confirm."+strconv.FormatInt(id, 10), key, confirmBody)
		if err != nil {
			return err
		}
		return printResponse(confirm)
	},
}

var orderDownloadCmd = &cobra.Command{
	Use:   "download <order-id>",
	Short: "Download a current or snapshot workspace file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := numericArgument(args, "order ID")
		if err != nil {
			return err
		}
		logicalPath, _ := cmd.Flags().GetString("path")
		destination, _ := cmd.Flags().GetString("output")
		if strings.TrimSpace(logicalPath) == "" || strings.TrimSpace(destination) == "" {
			return fmt.Errorf("--path and --output are required")
		}
		basePath := "/orders/" + strconv.FormatInt(id, 10)
		if snapshotID, _ := cmd.Flags().GetInt64("snapshot-id"); snapshotID > 0 {
			basePath += "/snapshots/" + strconv.FormatInt(snapshotID, 10) + "/download"
		} else {
			basePath += "/download"
		}
		client := newCommissionClient()
		response, err := client.Get(basePath, map[string]string{"path": logicalPath})
		if err != nil {
			return err
		}
		if response.Code != 0 {
			return fmt.Errorf("%s", response.Msg)
		}
		var data commissionapi.TransferGrantData
		if err := json.Unmarshal(response.Data, &data); err != nil {
			return fmt.Errorf("parse download grant: %w", err)
		}
		force, _ := cmd.Flags().GetBool("force")
		if err := commissionapi.Download(context.Background(), client.HTTPClient, data.Grant, destination, force); err != nil {
			return err
		}
		result := json.RawMessage(fmt.Sprintf(`{"path":%q,"logical_path":%q}`, destination, logicalPath))
		if resolveFormat() == "table" {
			return printTableJSON(result)
		}
		output.PrintData(result, resolveFormat())
		return nil
	},
}

func init() {
	orderCreateCmd.Flags().String("buyer-input", "", "buyer input matching the commission request specification")
	orderCreateCmd.Flags().String("impression-id", "", "discovery impression ID to attribute")
	addIdempotencyFlag(orderCreateCmd)
	orderListCmd.Flags().String("role", "", "order role filter")
	orderListCmd.Flags().String("state", "", "order state filter")
	orderListCmd.Flags().Int("limit", 20, "maximum orders")
	orderListCmd.Flags().String("cursor", "", "pagination cursor")
	orderReviewCmd.Flags().Int("score", 0, "review score (1-5)")
	orderReviewCmd.Flags().String("text", "", "optional review text")
	addIdempotencyFlag(orderReviewCmd)
	orderUploadCmd.Flags().String("file", "", "local file to upload")
	orderUploadCmd.Flags().String("path", "", "workspace logical path (defaults to local filename)")
	addIdempotencyFlag(orderUploadCmd)
	orderDownloadCmd.Flags().String("path", "", "workspace logical path")
	orderDownloadCmd.Flags().String("output", "", "local destination path")
	orderDownloadCmd.Flags().Int64("snapshot-id", 0, "snapshot ID (current workspace when omitted)")
	orderDownloadCmd.Flags().Bool("force", false, "replace an existing destination")
	orderCmd.AddCommand(orderCreateCmd, orderListCmd, orderGetCmd, orderSubmitMaterialsCmd,
		orderCancelCmd, orderAcceptCmd, orderRejectCmd, orderDeliverCmd, orderCompleteCmd,
		orderReviewCmd, orderGetReviewCmd, orderUploadCmd, orderDownloadCmd)
	rootCmd.AddCommand(orderCmd)
}
