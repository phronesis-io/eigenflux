package main

import "eigenflux_server/pkg/reqinfo"

// buildContextFeatures projects the request-scoped client metadata extracted
// from Kitex metainfo (set by the gateway's ClientInfoMiddleware) into the
// JSON shape stamped onto agent_features for replay-log analysis. Empty
// fields are omitted so dev requests with no client headers don't bloat the
// replay payload. Version numbers are emitted as ints so downstream queries
// can compare numerically without parsing strings.
//
// Source headers (set by ClientInfoMiddleware):
//
//	X-Client-Host, X-Client-Channel, X-Client-ID,
//	X-Client-OS,   X-Client-TZ,      X-Client-Lang,
//	X-CLI-Ver,     X-Skill-Ver
func buildContextFeatures(ci reqinfo.ClientInfo) map[string]any {
	feat := map[string]any{}
	if ci.Host != "" {
		feat["client_host"] = ci.Host
	}
	if ci.Channel != "" {
		feat["client_channel"] = ci.Channel
	}
	if ci.ClientID != "" {
		feat["client_id"] = ci.ClientID
	}
	if ci.OS != "" {
		feat["client_os"] = ci.OS
	}
	if ci.TZ != "" {
		feat["client_tz"] = ci.TZ
	}
	if ci.Lang != "" {
		feat["client_lang"] = ci.Lang
	}
	if ci.CLIVer != "" {
		feat["cli_ver"] = ci.CLIVer
	}
	if ci.CLIVerNum != 0 {
		feat["cli_ver_num"] = ci.CLIVerNum
	}
	if ci.SkillVer != "" {
		feat["skill_ver"] = ci.SkillVer
	}
	if ci.SkillVerNum != 0 {
		feat["skill_ver_num"] = ci.SkillVerNum
	}
	if len(feat) == 0 {
		return nil
	}
	return feat
}
