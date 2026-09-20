package tradebff

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// TradeOrderFile reads the submitted snapshot, never the mutable workspace.
func (s *Service) TradeOrderFile(ctx context.Context, c *app.RequestContext) {
	actor, ok := agentID(c)
	if !ok {
		replyError(c, 401, "CONSOLE_SESSION_REQUIRED", "Console Session 无效")
		return
	}
	orderID, snapshotID := c.Param("order_id"), c.Param("snapshot_id")
	logicalPath := string(c.Query("path"))
	preview := string(c.Query("preview")) == "1"
	if !positiveDecimal(orderID) || orderID != strings.TrimSpace(orderID) || !positiveDecimal(snapshotID) || snapshotID != strings.TrimSpace(snapshotID) ||
		logicalPath == "" || strings.ContainsAny(logicalPath, "\x00\r\n\\") || strings.HasPrefix(logicalPath, "/") || path.Clean(logicalPath) != logicalPath || logicalPath == ".." || strings.HasPrefix(logicalPath, "../") {
		replyError(c, 400, "INVALID_FILE_REQUEST", "文件参数无效")
		return
	}
	if preview && !strings.EqualFold(path.Ext(logicalPath), ".md") && !strings.EqualFold(path.Ext(logicalPath), ".txt") {
		replyError(c, 400, "FILE_PREVIEW_UNSUPPORTED", "此文件请下载查看")
		return
	}
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return
	}
	data, err := s.fetch(ctx, actor, "orders:files:read", "console.trade.orders.files.read", http.MethodGet,
		"/api/v1/orders/"+orderID+"/snapshots/"+snapshotID+"/download", url.Values{"path": {logicalPath}}, nil, "", false)
	if err != nil {
		replyError(c, upstreamStatus(err), "COMMISSION_FILE_UNAVAILABLE", "文件暂不可用，请重试")
		return
	}
	var value struct {
		Grant struct {
			Method    string            `json:"method"`
			URL       string            `json:"url"`
			Headers   map[string]string `json:"headers"`
			ExpiresAt int64             `json:"expires_at"`
		} `json:"grant"`
	}
	if json.Unmarshal(data, &value) != nil {
		replyError(c, 502, "INVALID_FILE_GRANT", "文件下载授权无效")
		return
	}
	grant := value.Grant
	u, err := url.Parse(grant.URL)
	// Only Commission-issued OSS grants are fetched. Browser-supplied URLs,
	// redirects, cookies and Console credentials never reach object storage.
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".aliyuncs.com") || grant.Method != http.MethodGet || grant.ExpiresAt <= time.Now().UnixMilli() {
		replyError(c, 502, "INVALID_FILE_GRANT", "文件下载授权无效")
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		replyError(c, 502, "INVALID_FILE_GRANT", "文件下载授权无效")
		return
	}
	for name, value := range grant.Headers {
		if strings.HasPrefix(strings.ToLower(name), "x-oss-") {
			req.Header.Set(name, value)
		}
	}
	client := *s.client.http
	client.Timeout = time.Minute
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		replyError(c, 503, "FILE_DOWNLOAD_FAILED", "文件读取失败，请重试")
		return
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		replyError(c, 502, "FILE_DOWNLOAD_FAILED", "文件读取失败，请重试")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if preview {
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil {
			replyError(c, 502, "FILE_DOWNLOAD_FAILED", "文件读取失败，请重试")
			return
		}
		if len(body) > 1<<20 {
			replyError(c, 413, "FILE_PREVIEW_TOO_LARGE", "文件较大，请下载查看全文")
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(logicalPath)}))
	c.Response.SetBodyStream(response.Body, -1)
}
