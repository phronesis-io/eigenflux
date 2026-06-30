package agti

// humanCardNames maps the engine/type code to the human-facing relationship name
// shown on the result page. MUST stay in sync with the frontend
// src/components/agti/agtiCards.js (CARDS[<code>].name): the result page renders
// the name from there, and the agent's interpret doc must show the SAME name.
// (The names in static/agti/types.json are the older, diverged labels and are
// only used for scoring keys — never as the human-facing display name.)
var humanCardNames = map[string]string{
	"SOUL":  "双生脑",
	"GANG":  "递刀者",
	"KISS":  "供奉者",
	"CTRL":  "被拿捏者",
	"MAMA":  "托儿所",
	"XRAY":  "过不了安检",
	"NPC":   "丢失关键帧",
	"FAKE":  "假熟",
	"VS-ER": "反骨仔",
	"????":  "电子谜语人",
}

// humanCardName returns the human-facing name for a type code, or "" if unknown.
func humanCardName(code string) string { return humanCardNames[code] }
