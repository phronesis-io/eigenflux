package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/dispatch"
)

const (
	commissionMaterialMaxFiles       = 128
	commissionMaterialMaxBytes int64 = 64 << 20
)

var (
	errCommissionMaterialsMissing     = errors.New("commission_materials_missing")
	errCommissionMaterialsInvalid     = errors.New("commission_materials_invalid")
	errCommissionMaterialsUnavailable = errors.New("commission_materials_unavailable")
	errCommissionMaterialsLimit       = fmt.Errorf("commission_materials_limit: %w", dispatch.ErrNeedsUser)
)

type commissionLocalFile struct {
	LogicalPath string `json:"logical_path"`
	Path        string `json:"path"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
}

type commissionMaterialEntry struct {
	LogicalPath string         `json:"logical_path"`
	ObjectID    notificationID `json:"object_id"`
	SnapshotID  notificationID `json:"snapshot_id"`
	ByteSize    *int64         `json:"byte_size"`
	SHA256      string         `json:"sha256"`
	UploaderID  notificationID `json:"uploader_agent_id"`
}

// prepareCommissionMaterials admits only the pinned seller's input manifest and
// verifies downloaded bytes before exposing local paths to the Agent. The caller
// owns directory cleanup, including any earlier files when a later transfer fails.
func (w *accountWatch) prepareCommissionMaterials(ctx context.Context, order commissionIntakeOrder, directory string) ([]commissionLocalFile, error) {
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return nil, err
	}
	if order.OrderID <= 0 || order.BuyerID <= 0 || order.Version <= 0 || strconv.FormatInt(int64(order.SellerID), 10) != w.binding.AgentID {
		return nil, errCommissionMaterialsInvalid
	}
	consoleAPI := *api
	consoleAPI.BaseURL = strings.TrimSuffix(api.BaseURL, "/api/v1") + "/api/v2"
	id := strconv.FormatInt(int64(order.OrderID), 10)
	response, err := consoleAPI.Get("/console/trade/orders/"+id, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errCommissionMaterialsUnavailable
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
	if response.Code != 0 || len(response.Data) > 256<<10 || json.Unmarshal(response.Data, &detail) != nil || detail.OrderID != order.OrderID || detail.Role != "seller" || detail.Version != order.Version || detail.State != order.State || detail.Counterparty.AgentID != order.BuyerID || detail.Files.Input == nil {
		return nil, errCommissionMaterialsInvalid
	}
	if err := w.commissionIdentityOK(); err != nil {
		return nil, err
	}
	if len(detail.Files.Input) > commissionMaterialMaxFiles {
		return nil, errCommissionMaterialsLimit
	}
	seen := map[string]bool{}
	total := int64(0)
	for _, file := range detail.Files.Input {
		if !validCommissionMaterialPath(file.LogicalPath) || seen[file.LogicalPath] || file.ObjectID <= 0 || file.SnapshotID <= 0 || file.UploaderID != order.BuyerID || file.ByteSize == nil || *file.ByteSize < 0 {
			return nil, errCommissionMaterialsInvalid
		}
		if digest, err := hex.DecodeString(file.SHA256); err != nil || len(digest) != sha256.Size {
			return nil, errCommissionMaterialsInvalid
		}
		if *file.ByteSize > commissionMaterialMaxBytes-total {
			return nil, errCommissionMaterialsLimit
		}
		total += *file.ByteSize
		seen[file.LogicalPath] = true
	}
	localDirectory, err := filepath.Abs(directory)
	if err != nil {
		return nil, errCommissionMaterialsUnavailable
	}
	info, err := os.Lstat(localDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errCommissionMaterialsUnavailable
	}
	files := make([]commissionLocalFile, 0, len(detail.Files.Input))
	for _, file := range detail.Files.Input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Refresh and identity checks occur on the fixed account before every grant.
		api, err = w.commissionAPI(ctx)
		if err != nil {
			return nil, err
		}
		response, err = api.Get("/orders/"+id+"/snapshots/"+strconv.FormatInt(int64(file.SnapshotID), 10)+"/download", map[string]string{"path": file.LogicalPath})
		if err != nil {
			return nil, commissionMaterialRequestError(ctx, err)
		}
		var data commissionapi.TransferGrantData
		// The current download service omits ObjectID; snapshots and verified
		// bytes identify the object. If a newer grant supplies it, require a match.
		if response.Code != 0 || len(response.Data) > 64<<10 || json.Unmarshal(response.Data, &data) != nil || (data.Grant.ObjectID != 0 && data.Grant.ObjectID != int64(file.ObjectID)) {
			return nil, errCommissionMaterialsInvalid
		}
		if err := w.commissionIdentityOK(); err != nil {
			return nil, err
		}
		local, err := downloadCommissionMaterial(ctx, api.HTTPClient, data.Grant, file, localDirectory)
		if err != nil {
			return nil, err
		}
		files = append(files, local)
	}
	return files, nil
}

func validCommissionMaterialPath(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && len(value) <= 1024 && strings.IndexFunc(value, unicode.IsControl) < 0 && !strings.Contains(value, `\`) && !path.IsAbs(value) && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func commissionMaterialRequestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiError *client.APIError
	if errors.As(err, &apiError) && (apiError.StatusCode == http.StatusNotFound || apiError.StatusCode == http.StatusGone) {
		return errCommissionMaterialsMissing
	}
	// API/network errors may contain signed URLs or server-provided private text.
	return errCommissionMaterialsUnavailable
}

func downloadCommissionMaterial(ctx context.Context, base *http.Client, grant commissionapi.TransferGrant, file commissionMaterialEntry, directory string) (commissionLocalFile, error) {
	var local commissionLocalFile
	target, err := url.Parse(grant.URL)
	if err != nil || target.Hostname() == "" || (target.Scheme != "https" && target.Scheme != "http") || target.User != nil || target.Fragment != "" || grant.Method != http.MethodGet {
		return local, errCommissionMaterialsInvalid
	}
	if grant.ExpiresAt > 0 {
		if !time.UnixMilli(grant.ExpiresAt).After(time.Now()) {
			return local, errCommissionMaterialsUnavailable
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(grant.ExpiresAt))
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return local, errCommissionMaterialsInvalid
	}
	for key, value := range grant.Headers {
		switch strings.ToLower(key) {
		case "authorization", "proxy-authorization", "cookie", "host":
			return local, errCommissionMaterialsInvalid
		}
		request.Header.Set(key, value)
	}
	// A transfer client has neither an API bearer nor a cookie jar. Do not follow
	// redirects that could forward signed headers to a different destination.
	transfer := &http.Client{Transport: base.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := transfer.Do(request)
	if err != nil {
		return local, commissionMaterialRequestError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return local, errCommissionMaterialsMissing
	}
	if response.StatusCode != http.StatusOK || (response.ContentLength >= 0 && response.ContentLength != *file.ByteSize) {
		return local, errCommissionMaterialsInvalid
	}
	ext := path.Ext(file.LogicalPath)
	if len(ext) > 16 || strings.IndexFunc(ext, func(r rune) bool {
		return r != '.' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9')
	}) >= 0 {
		ext = ""
	}
	destination, err := os.CreateTemp(directory, "input-*"+ext)
	if err != nil {
		return local, errCommissionMaterialsUnavailable
	}
	verified := false
	defer func() {
		_ = destination.Close()
		if !verified {
			_ = os.Remove(destination.Name())
		}
	}()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(destination, digest), io.LimitReader(response.Body, *file.ByteSize+1))
	if err != nil {
		return local, commissionMaterialRequestError(ctx, err)
	}
	if size != *file.ByteSize || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), file.SHA256) {
		return local, errCommissionMaterialsInvalid
	}
	if err := destination.Close(); err != nil {
		return local, errCommissionMaterialsUnavailable
	}
	verified = true
	return commissionLocalFile{LogicalPath: file.LogicalPath, Path: destination.Name(), ByteSize: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}
