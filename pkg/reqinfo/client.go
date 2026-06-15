package reqinfo

import (
	"context"
	"strconv"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

const (
	KeySkillVer      = "ef.skill_ver"
	KeySkillVerNum   = "ef.skill_ver_num"
	KeyCLIVer        = "ef.cli_ver"
	KeyCLIVerNum     = "ef.cli_ver_num"
	KeyClientOS      = "ef.client_os"
	KeyClientTZ      = "ef.client_tz"
	KeyClientLang    = "ef.client_lang"
	KeyClientHost    = "ef.client_host"
	KeyClientChannel = "ef.client_channel"
	KeyClientID      = "ef.client_id"
	// Raw model identifier the agent runs as (X-Client-Model), e.g.
	// "claude-opus-4-8". Reported on the nightly settings push.
	KeyClientModel = "ef.client_model"
	// Per-request provenance for a bio update (X-Bio-Source / X-Bio-Note):
	// which sources the agent drew on ("memory,session,broadcast") and a
	// one-line rationale. Only present on `profile update` calls.
	KeyBioSource = "ef.bio_source"
	KeyBioNote   = "ef.bio_note"
)

type ClientInfo struct {
	SkillVer    string
	SkillVerNum int
	CLIVer      string
	CLIVerNum   int
	OS          string
	TZ          string
	Lang        string
	Host        string
	Channel     string
	ClientID    string
	Model       string
}

func ClientFromContext(ctx context.Context) ClientInfo {
	var c ClientInfo
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVer); ok {
		c.SkillVer = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVerNum); ok {
		c.SkillVerNum, _ = strconv.Atoi(v)
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyCLIVer); ok {
		c.CLIVer = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyCLIVerNum); ok {
		c.CLIVerNum, _ = strconv.Atoi(v)
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientOS); ok {
		c.OS = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientTZ); ok {
		c.TZ = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientLang); ok {
		c.Lang = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientHost); ok {
		c.Host = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientChannel); ok {
		c.Channel = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientID); ok {
		c.ClientID = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientModel); ok {
		c.Model = v
	}
	return c
}

// BioProvenance carries the agent's self-reported provenance for a bio update,
// read from the X-Bio-Source / X-Bio-Note request headers. Both fields are
// empty when the client did not supply them (e.g. a manual `profile update`).
type BioProvenance struct {
	Source string
	Note   string
}

// BioProvenanceFromContext extracts the bio update provenance from request
// metadata. Used by the profile service to annotate agent_bio_history rows.
func BioProvenanceFromContext(ctx context.Context) BioProvenance {
	var p BioProvenance
	if v, ok := metainfo.GetPersistentValue(ctx, KeyBioSource); ok {
		p.Source = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyBioNote); ok {
		p.Note = v
	}
	return p
}

func (c ClientInfo) ToVars() map[string]string {
	return map[string]string{
		"skill_ver":      c.SkillVer,
		"skill_ver_num":  strconv.Itoa(c.SkillVerNum),
		"cli_ver":        c.CLIVer,
		"cli_ver_num":    strconv.Itoa(c.CLIVerNum),
		"client_os":      c.OS,
		"client_tz":      c.TZ,
		"client_lang":    c.Lang,
		"client_host":    c.Host,
		"client_channel": c.Channel,
		"client_id":      c.ClientID,
		"client_model":   c.Model,
	}
}
