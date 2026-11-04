// Command integration_admin manages enterprise applications through operator DB
// access. It is read-only/dry-run by default and never creates customer routes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/integrationidentity"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("integration_admin", flag.ContinueOnError)
	action := flags.String("action", "inspect", "inspect, audit, create-app, rotate-credential, set-enabled, set-policy")
	appID := flags.String("app-id", "", "application identifier")
	agentID := flags.Int64("agent-id", 0, "managed internal Agent ID")
	afterID := flags.Int64("after-agent-id", 0, "Agent inspection page cursor")
	installationID := flags.String("installation-id", "", "installation identifier")
	scope := flags.String("scope", "app", "set-enabled target: global, app, agent, installation")
	enabled := flags.String("enabled", "", "explicit true or false")
	actor := flags.String("actor", "", "operator identity for mutations")
	reason := flags.String("reason", "", "operator reason for mutations")
	apply := flags.Bool("apply", false, "execute a mutation; default is a dry-run plan")
	name := flags.String("name", "", "application display name")
	credentialOut := flags.String("credential-out", "", "new, exclusive mode-0600 credential file")
	maxAgents := flags.Int("max-agents", 1, "approved Agent slots")
	maxInstallations := flags.Int("max-installations", 1, "installation slots per Agent")
	minPoll := flags.Int("min-poll-seconds", 3600, "minimum Feed interval")
	daily := flags.Int("max-feed-batches-per-day", 10000, "application daily generation admissions")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *agentID < 0 || *afterID < 0 {
		return errors.New("Agent identifiers must be nonnegative")
	}
	write := *action != "inspect" && *action != "audit"
	if write && (strings.TrimSpace(*actor) == "" || strings.TrimSpace(*reason) == "") {
		return errors.New("--actor and --reason required for mutation plans and apply")
	}
	if len(*actor) > 128 || len(*reason) > 512 {
		return errors.New("actor/reason too long")
	}
	policy := integrationidentity.AppPolicy{MaxAgents: *maxAgents, MaxInstallationsPerAgent: *maxInstallations, MinPollIntervalSeconds: *minPoll, MaxFeedBatchesPerDay: *daily}
	switch *action {
	case "inspect", "audit":
	case "create-app":
		if *appID == "" || strings.TrimSpace(*name) == "" {
			return errors.New("--app-id and --name required")
		}
	case "rotate-credential", "set-policy":
		if *appID == "" {
			return errors.New("--app-id required")
		}
	case "set-enabled":
		if *enabled != "true" && *enabled != "false" {
			return errors.New("--enabled must be explicit true or false")
		}
		switch *scope {
		case "global":
			if *appID != "" || *agentID != 0 || *installationID != "" {
				return errors.New("global scope does not accept app/Agent/installation targets")
			}
		case "app":
			if *appID == "" || *agentID != 0 || *installationID != "" {
				return errors.New("app scope requires only --app-id")
			}
		case "agent":
			if *appID == "" || *agentID == 0 || *installationID != "" {
				return errors.New("agent scope requires --app-id and --agent-id")
			}
		case "installation":
			if *appID == "" || *installationID == "" {
				return errors.New("installation scope requires --app-id and --installation-id")
			}
		default:
			return errors.New("invalid --scope")
		}
	default:
		return errors.New("unknown --action")
	}
	if *action == "create-app" || *action == "set-policy" {
		if policy.MaxAgents < 1 || policy.MaxInstallationsPerAgent < 1 || policy.MinPollIntervalSeconds < 10 || policy.MinPollIntervalSeconds > 86400 || policy.MaxFeedBatchesPerDay < 1 {
			return errors.New("invalid positive quotas or interval (10..86400 seconds)")
		}
	}
	if (*action == "create-app" || *action == "rotate-credential") && *apply && *credentialOut == "" {
		return errors.New("--credential-out required; credentials are never printed")
	}
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		return errors.New("PG_DSN must be explicitly supplied in the environment")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return errors.New("operator database unavailable")
	}
	connection, err := database.DB()
	if err != nil {
		return errors.New("operator database unavailable")
	}
	defer connection.Close()
	if err := connection.PingContext(ctx); err != nil {
		return errors.New("operator database unavailable")
	}
	store := &integrationidentity.Store{DB: database}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if *action == "inspect" {
		result, err := store.Inspect(ctx, *appID, *agentID, *afterID)
		if err != nil {
			return errors.New("inspection failed; apply current migrations and verify target/database")
		}
		return encoder.Encode(result)
	}
	if *action == "audit" {
		var rows []struct {
			ID         int64  `json:"id"`
			AppID      string `json:"app_id"`
			AgentID    *int64 `json:"agent_id,omitempty,string"`
			Actor      string `json:"actor"`
			Operation  string `json:"operation"`
			Reason     string `json:"reason"`
			OccurredAt int64  `json:"occurred_at"`
		}
		query := database.WithContext(ctx).Table("integration_audit")
		if *appID != "" {
			query = query.Where("app_id = ?", *appID)
		}
		if *agentID != 0 {
			query = query.Where("agent_id = ?", *agentID)
		}
		if err := query.Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
			return errors.New("audit inspection failed")
		}
		return encoder.Encode(rows)
	}
	if *action == "set-policy" {
		current, err := store.AppPolicy(ctx, *appID)
		if err != nil {
			return errors.New("application policy unavailable")
		}
		changed := map[string]bool{}
		flags.Visit(func(value *flag.Flag) { changed[value.Name] = true })
		if !changed["max-agents"] {
			policy.MaxAgents = current.MaxAgents
		}
		if !changed["max-installations"] {
			policy.MaxInstallationsPerAgent = current.MaxInstallationsPerAgent
		}
		if !changed["min-poll-seconds"] {
			policy.MinPollIntervalSeconds = current.MinPollIntervalSeconds
		}
		if !changed["max-feed-batches-per-day"] {
			policy.MaxFeedBatchesPerDay = current.MaxFeedBatchesPerDay
		}
		if !changed["max-agents"] && !changed["max-installations"] && !changed["min-poll-seconds"] && !changed["max-feed-batches-per-day"] {
			return errors.New("set-policy requires at least one policy option")
		}
	}
	plan := map[string]any{"action": *action, "apply": *apply, "app_id": *appID, "agent_id": strconv.FormatInt(*agentID, 10), "scope": *scope, "installation_id": *installationID, "enabled": *enabled, "actor": *actor, "reason": *reason}
	if *action == "create-app" || *action == "set-policy" {
		plan["policy"] = policy
	}
	if !*apply {
		plan["message"] = "dry-run; no data changed"
		return encoder.Encode(plan)
	}
	var credentialFile *os.File
	if *action == "create-app" || *action == "rotate-credential" {
		credentialFile, err = os.OpenFile(*credentialOut, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("credential output must be a new writable file")
		}
		defer credentialFile.Close()
	}
	var secret string
	switch *action {
	case "create-app":
		secret, err = store.CreateApp(ctx, integrationidentity.App{AppID: *appID, Name: *name, MaxAgents: policy.MaxAgents, MaxInstallationsPerAgent: policy.MaxInstallationsPerAgent, MinPollIntervalSeconds: policy.MinPollIntervalSeconds}, *actor)
	case "rotate-credential":
		secret, err = store.RotateCredential(ctx, *appID, *actor, *reason)
	case "set-policy":
		err = store.SetAppPolicy(ctx, *appID, policy, *actor, *reason)
	case "set-enabled":
		if *scope == "installation" {
			err = store.SetInstallationEnabled(ctx, *appID, *installationID, *enabled == "true", *actor, *reason)
		} else {
			err = store.SetEnabled(ctx, *appID, *agentID, *enabled == "true", *actor, *reason)
		}
	}
	if err != nil {
		if credentialFile != nil {
			_ = os.Remove(*credentialOut)
		}
		return errors.New("operator change failed; verify target, policy and database")
	}
	if credentialFile != nil {
		if _, err := io.WriteString(credentialFile, secret+"\n"); err != nil {
			return errors.New("credential committed but file write failed; rotate it to recover")
		}
		if err := credentialFile.Sync(); err != nil {
			return errors.New("credential committed but file sync failed; rotate it to recover")
		}
		plan["credential_file"] = *credentialOut
	}
	if *action == "create-app" {
		if err := store.SetAppPolicy(ctx, *appID, policy, *actor, *reason); err != nil {
			return errors.New("application created disabled and credential saved; policy setup failed, inspect before enabling")
		}
		plan["enabled"] = false
	}
	plan["message"] = "applied and audited"
	return encoder.Encode(plan)
}
