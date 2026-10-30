package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/commissionapi"
)

// Only immutable inputs already uploaded by this order's bound buyer are made
// available to clarification. Grants and byte verification share the seller's
// transfer boundary without extending seller-only authorization or file writes.
func (w *accountWatch) prepareBuyerClarificationMaterials(ctx context.Context, order commissionIntakeOrder, directory string) ([]commissionLocalFile, error) {
	if order.OrderID <= 0 || order.Version <= 0 || order.SnapshotID <= 0 || order.SellerID <= 0 || strconv.FormatInt(int64(order.BuyerID), 10) != w.binding.AgentID || order.State != "awaiting_seller" {
		return nil, errCommissionMaterialsInvalid
	}
	info, err := os.Lstat(directory)
	if !filepath.IsAbs(directory) || err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errCommissionMaterialsInvalid
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return nil, err
	}
	consoleAPI := *api
	consoleAPI.BaseURL = strings.TrimSuffix(api.BaseURL, "/api/v1") + "/api/v2"
	id := strconv.FormatInt(int64(order.OrderID), 10)
	response, err := consoleAPI.Get("/console/trade/orders/"+id, nil)
	if err != nil {
		return nil, commissionMaterialRequestError(ctx, err)
	}
	var detail struct {
		OrderID      notificationID `json:"order_id"`
		Role         string         `json:"role"`
		Version      int64          `json:"version"`
		State        string         `json:"state"`
		Counterparty struct {
			AgentID notificationID `json:"agent_id"`
		} `json:"counterparty"`
		Files struct {
			Input []commissionMaterialEntry `json:"input"`
		} `json:"files"`
	}
	if response.Code != 0 || len(response.Data) > 256<<10 || json.Unmarshal(response.Data, &detail) != nil || detail.OrderID != order.OrderID || detail.Role != "buyer" || detail.Version != order.Version || detail.State != order.State || detail.Counterparty.AgentID != order.SellerID || detail.Files.Input == nil {
		return nil, errCommissionMaterialsInvalid
	}
	if err := w.commissionIdentityOK(); err != nil {
		return nil, err
	}
	if len(detail.Files.Input) > commissionMaterialMaxFiles {
		return nil, errCommissionMaterialsLimit
	}
	seen, total := map[string]bool{}, int64(0)
	for _, file := range detail.Files.Input {
		if !validCommissionMaterialPath(file.LogicalPath) || !validCommissionArtifactPath(file.LogicalPath) || seen[file.LogicalPath] || file.ObjectID <= 0 || file.SnapshotID <= 0 || file.UploaderID != order.BuyerID || file.ByteSize == nil || *file.ByteSize < 0 {
			return nil, errCommissionMaterialsInvalid
		}
		if digest, err := hex.DecodeString(file.SHA256); err != nil || len(digest) != sha256.Size {
			return nil, errCommissionMaterialsInvalid
		}
		if *file.ByteSize > commissionMaterialMaxBytes-total {
			return nil, errCommissionMaterialsLimit
		}
		seen[file.LogicalPath] = true
		total += *file.ByteSize
	}
	files := make([]commissionLocalFile, 0, len(detail.Files.Input))
	for _, file := range detail.Files.Input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err = w.commissionAPI(ctx)
		if err != nil {
			return nil, err
		}
		response, err = api.Get("/orders/"+id+"/snapshots/"+strconv.FormatInt(int64(file.SnapshotID), 10)+"/download", map[string]string{"path": file.LogicalPath})
		if err != nil {
			return nil, commissionMaterialRequestError(ctx, err)
		}
		var data commissionapi.TransferGrantData
		if response.Code != 0 || len(response.Data) > 64<<10 || json.Unmarshal(response.Data, &data) != nil || data.Grant.ObjectID != 0 && data.Grant.ObjectID != int64(file.ObjectID) {
			return nil, errCommissionMaterialsInvalid
		}
		if err := w.commissionIdentityOK(); err != nil {
			return nil, err
		}
		local, err := downloadCommissionMaterial(ctx, api.HTTPClient, data.Grant, file, directory)
		if err != nil {
			return nil, err
		}
		if err := w.commissionIdentityOK(); err != nil {
			return nil, err
		}
		files = append(files, local)
	}
	return files, nil
}
