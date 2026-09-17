package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/commissionapi"
	"github.com/spf13/cobra"
)

type materialFile struct {
	LogicalPath string `json:"logical_path"`
	SHA256      string `json:"sha256"`
	ByteSize    int64  `json:"byte_size"`
	LocalPath   string `json:"-"`
}

func inspectMaterial(logical, local string) (materialFile, error) {
	if logical == "" || path.Clean(logical) != logical || strings.HasPrefix(logical, "/") || logical == ".." || strings.HasPrefix(logical, "../") || strings.ContainsAny(logical, "\\\x00") {
		return materialFile{}, fmt.Errorf("invalid material logical path")
	}
	size, digest, err := commissionapi.FileDigest(local)
	if err != nil {
		return materialFile{}, err
	}
	return materialFile{LogicalPath: logical, SHA256: digest, ByteSize: size, LocalPath: local}, nil
}
func textMaterial(inline, filename, logical string) (materialFile, func(), error) {
	cleanup := func() {}
	if inline != "" && filename != "" {
		return materialFile{}, cleanup, fmt.Errorf("text and text-file options are mutually exclusive")
	}
	if filename != "" {
		data, err := os.ReadFile(filename)
		if err != nil {
			return materialFile{}, cleanup, err
		}
		if !utf8.Valid(data) {
			return materialFile{}, cleanup, fmt.Errorf("text material must be UTF-8")
		}
	} else {
		if !utf8.ValidString(inline) {
			return materialFile{}, cleanup, fmt.Errorf("text material must be UTF-8")
		}
		file, err := os.CreateTemp("", "eigenflux-material-*.txt")
		if err != nil {
			return materialFile{}, cleanup, err
		}
		filename = file.Name()
		cleanup = func() { _ = os.Remove(filename) }
		if _, err := file.WriteString(inline); err != nil {
			file.Close()
			cleanup()
			return materialFile{}, func() {}, err
		}
		if err := file.Close(); err != nil {
			cleanup()
			return materialFile{}, func() {}, err
		}
	}
	material, err := inspectMaterial(logical, filename)
	return material, cleanup, err
}
func childMutationKey(master, operation string) string {
	hash := sha256.Sum256([]byte(master + "\x00" + operation))
	return hex.EncodeToString(hash[:])
}
func uploadMaterial(ctx context.Context, c *client.Client, base, master string, file materialFile) error {
	if ctx == nil {
		ctx = context.Background()
	}
	body := map[string]any{"logical_path": file.LogicalPath, "byte_size": file.ByteSize, "sha256": file.SHA256}
	response, err := postMutation(c, base+"/uploads", "order.material", childMutationKey(master, "upload:"+file.LogicalPath), body)
	if err != nil {
		return err
	}
	if response.Code != 0 {
		return fmt.Errorf("%s", response.Msg)
	}
	var data commissionapi.TransferGrantData
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return err
	}
	if data.Grant.ObjectID <= 0 {
		return fmt.Errorf("incomplete material upload grant")
	}
	if data.Grant.URL != "" {
		if err := commissionapi.Upload(ctx, c.HTTPClient, data.Grant, file.LocalPath); err != nil {
			return err
		}
	} else if data.Grant.Method != "" {
		return fmt.Errorf("incomplete material upload grant")
	}
	confirmed, err := postMutation(c, base+"/uploads/confirm", "order.material.confirm", childMutationKey(master, "confirm:"+file.LogicalPath), map[string]any{"object_id": data.Grant.ObjectID})
	if err != nil {
		return err
	}
	if confirmed.Code != 0 {
		return fmt.Errorf("%s", confirmed.Msg)
	}
	return nil
}
func createOrderWithMaterials(cmd *cobra.Command, args []string) error {
	id, err := numericArgument(args, "commission ID")
	if err != nil {
		return err
	}
	specs, _ := cmd.Flags().GetStringArray("input-file")
	files := make([]materialFile, 0, len(specs)+1)
	for _, spec := range specs {
		logical, local, ok := strings.Cut(spec, "=")
		if !ok {
			return fmt.Errorf("--input-file requires LOGICAL_PATH=LOCAL_FILE")
		}
		file, err := inspectMaterial(logical, local)
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	inline, _ := cmd.Flags().GetString("buyer-input")
	filename, _ := cmd.Flags().GetString("buyer-input-file")
	if inline != "" || filename != "" {
		file, cleanup, err := textMaterial(inline, filename, "inputs/request.txt")
		defer cleanup()
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].LogicalPath < files[j].LogicalPath })
	for i := 1; i < len(files); i++ {
		if files[i-1].LogicalPath == files[i].LogicalPath {
			return fmt.Errorf("duplicate input logical path %s", files[i].LogicalPath)
		}
	}
	body := map[string]any{"commission_id": id}
	impression, _ := cmd.Flags().GetString("impression-id")
	if impression != "" {
		body["impression_id"] = impression
	}
	explicit, _ := cmd.Flags().GetString("idempotency-key")
	preparationID, _ := cmd.Flags().GetInt64("preparation-id")
	if preparationID < 0 {
		return fmt.Errorf("invalid preparation ID")
	}
	c := newCommissionClient()
	if len(files) == 0 && preparationID == 0 {
		resp, err := postMutation(c, "/orders", "order.create", explicit, body)
		if err != nil {
			return err
		}
		return printResponse(resp)
	}
	body["input_files"] = files
	master, err := mutationKey(explicit, "order.create.materials", body)
	if err != nil {
		return err
	}
	var prepared *client.APIResponse
	if preparationID > 0 {
		prepared, err = c.Get("/order-preparations/"+strconv.FormatInt(preparationID, 10), nil)
	} else {
		prepared, err = postMutation(c, "/order-preparations", "order.prepare", childMutationKey(master, "prepare"), body)
	}
	if err != nil {
		return err
	}
	if prepared.Code != 0 {
		return fmt.Errorf("%s", prepared.Msg)
	}
	var data struct {
		PreparationID int64           `json:"preparation_id"`
		Order         json.RawMessage `json:"order"`
	}
	if err = json.Unmarshal(prepared.Data, &data); err != nil {
		return err
	}
	if data.PreparationID <= 0 {
		return fmt.Errorf("incomplete order preparation")
	}
	preparationID = data.PreparationID
	finalized := len(data.Order) > 0 && string(data.Order) != "null"
	if !finalized {
		for _, file := range files {
			if err := uploadMaterial(cmd.Context(), c, "/order-preparations/"+strconv.FormatInt(preparationID, 10), master, file); err != nil {
				return fmt.Errorf("preparation %d: %w; resume with the same files/key and --preparation-id %d", preparationID, err, preparationID)
			}
		}
	}
	body["preparation_id"] = preparationID
	resp, err := postMutation(c, "/orders", "order.create", childMutationKey(master, "finalize"), body)
	if err != nil {
		return fmt.Errorf("preparation %d finalization: %w; retry the identical command", preparationID, err)
	}
	return printResponse(resp)
}
func uploadDeliveryText(cmd *cobra.Command, id int64, explicit string) ([]materialFile, error) {
	inline, _ := cmd.Flags().GetString("text")
	filename, _ := cmd.Flags().GetString("text-file")
	if inline == "" && filename == "" {
		return nil, nil
	}
	file, cleanup, err := textMaterial(inline, filename, "outputs/result.txt")
	defer cleanup()
	if err != nil {
		return nil, err
	}
	master, err := mutationKey(explicit, "order.delivery.text", map[string]any{"order_id": id, "file": file})
	if err != nil {
		return nil, err
	}
	c := newCommissionClient()
	base := "/orders/" + strconv.FormatInt(id, 10)
	response, err := c.Get(base, nil)
	if err != nil {
		return nil, err
	}
	if response.Code != 0 {
		return nil, fmt.Errorf("%s", response.Msg)
	}
	var data struct {
		Order struct {
			State string `json:"state"`
		} `json:"order"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return nil, err
	}
	// After a lost delivery response, replay the identical command without attempting another write.
	if data.Order.State == "in_progress" {
		if err := uploadMaterial(cmd.Context(), c, base, master, file); err != nil {
			return nil, err
		}
	}
	return []materialFile{file}, nil
}
