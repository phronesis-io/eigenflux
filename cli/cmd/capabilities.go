package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	capabilitycache "cli.eigenflux.ai/internal/capabilities"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var capabilityOperationIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

func validateCapabilityRegistry(raw json.RawMessage, language string) error {
	var registry struct {
		SchemaVersion int    `json:"schema_version"`
		Language      string `json:"language"`
		Operations    []struct {
			OperationID string `json:"operation_id"`
			CLI         string `json:"cli"`
		} `json:"operations"`
	}
	if json.Unmarshal(raw, &registry) != nil || registry.SchemaVersion != 1 || registry.Language != language || len(registry.Operations) == 0 {
		return fmt.Errorf("invalid capability registry response")
	}
	seen := make(map[string]bool, len(registry.Operations))
	for _, operation := range registry.Operations {
		if !capabilityOperationIDPattern.MatchString(operation.OperationID) || seen[operation.OperationID] {
			return fmt.Errorf("invalid capability operation ID")
		}
		seen[operation.OperationID] = true
		if !strings.HasPrefix(operation.CLI, "eigenflux ") || strings.ContainsAny(operation.CLI, "\r\n;&|`$<>") {
			return fmt.Errorf("invalid capability CLI mapping for %s", operation.OperationID)
		}
	}
	return nil
}

func capabilityCacheMaxAge(header http.Header) int64 {
	for _, directive := range strings.Split(header.Get("Cache-Control"), ",") {
		name, value, found := strings.Cut(strings.TrimSpace(directive), "=")
		if found && strings.EqualFold(name, "max-age") {
			seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil && seconds > 0 {
				return seconds
			}
		}
	}
	return 300
}

var capabilitiesCmd = &cobra.Command{
	Use:   "capabilities",
	Short: "Discover the current Agent and CLI capability registry",
	Long: `Fetch the server-managed capability registry, including stable operation IDs,
CLI mappings, risk and confirmation rules, bilingual semantic hints, editable
Agent Card fields, and protected fields. Cosmetic Console copy changes do not
change operation IDs. The last verified response is cached with its ETag.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		language, _ := cmd.Flags().GetString("lang")
		if language == "" {
			language = clientMeta.Lang
		}
		if strings.HasPrefix(strings.ToLower(language), "zh") {
			language = "zh-CN"
		} else if strings.HasPrefix(strings.ToLower(language), "en") {
			language = "en"
		} else {
			return fmt.Errorf("--lang must be zh-CN or en")
		}
		clientV2, server, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return err
		}
		serverURL := strings.TrimRight(server.Endpoint, "/")
		cached, cacheErr := capabilitycache.Load(server.Name, serverURL, credentials.AgentID, language)
		headers := map[string]string{}
		if cacheErr == nil {
			if validationErr := validateCapabilityRegistry(cached.Registry, language); validationErr != nil {
				cacheErr = validationErr
			} else if time.Now().UnixMilli()-cached.FetchedAt < cached.MaxAgeSeconds*int64(time.Second/time.Millisecond) {
				output.PrintData(json.RawMessage(cached.Registry), resolveFormat())
				return nil
			} else {
				headers["If-None-Match"] = cached.ETag
			}
		}
		response, err := clientV2.GetWithHeaders("/agent-capabilities", map[string]string{"lang": language}, headers)
		if err != nil {
			return err
		}
		if response.HTTPStatus == http.StatusNotModified {
			if cacheErr != nil {
				return fmt.Errorf("server returned 304 without a local capability registry")
			}
			cached.FetchedAt = time.Now().UnixMilli()
			cached.MaxAgeSeconds = capabilityCacheMaxAge(response.Header)
			if err := capabilitycache.Save(server.Name, cached); err != nil {
				return err
			}
			output.PrintData(json.RawMessage(cached.Registry), resolveFormat())
			return nil
		}
		if len(response.Data) == 0 {
			return fmt.Errorf("capability registry response is empty")
		}
		if err := validateCapabilityRegistry(response.Data, language); err != nil {
			return err
		}
		etag := response.Header.Get("ETag")
		if etag == "" {
			return fmt.Errorf("capability registry response has no ETag")
		}
		if err := capabilitycache.Save(server.Name, capabilitycache.Snapshot{
			OwnerAgentID: credentials.AgentID, ServerURL: serverURL, Language: language, ETag: etag,
			FetchedAt: time.Now().UnixMilli(), MaxAgeSeconds: capabilityCacheMaxAge(response.Header), Registry: response.Data,
		}); err != nil {
			return err
		}
		output.PrintData(json.RawMessage(response.Data), resolveFormat())
		return nil
	},
}

func init() {
	capabilitiesCmd.Flags().String("lang", "", "localized semantics: zh-CN or en (default: current client language)")
	rootCmd.AddCommand(capabilitiesCmd)
}
