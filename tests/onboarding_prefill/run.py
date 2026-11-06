#!/usr/bin/env python3
"""Prepare isolated Agent inputs and validate freshly generated onboarding drafts."""

import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CASES = Path(__file__).with_name("cases.json")
SOURCES = (
    "skills/ef-onboarding/SKILL.md",
    "skills/ef-onboarding/references/prefill.md",
)


def fingerprint():
    paths = (*SOURCES, str(CASES.relative_to(ROOT)))
    return {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in paths}


def prepare(output):
    output.mkdir(parents=True, exist_ok=False)
    cases = json.loads(CASES.read_text())
    skill = "\n\n".join(f"## {name}\n{(ROOT / name).read_text()}" for name in SOURCES)
    for case in cases:
        prompt = (
            "Generate an onboarding personalized draft in Chinese using the Skill below.\n"
            "Installation, identity and permissions are already satisfied for this simulation.\n"
            "The supplied context is the complete authorized retrieval result. Perform only\n"
            "draft generation. Do not retrieve other context, use accounts, run provisioning,\n"
            "upload, contact anyone, or take external actions. Return only the complete JSON\n"
            "draft, without Markdown fences or commentary.\n\n"
            + skill
            + "\n\n## Retrieved user context\n"
            + case["context"]
        )
        (output / f'{case["id"]}.prompt.txt').write_text(prompt)
    (output / "source-hashes.json").write_text(json.dumps(fingerprint(), indent=2) + "\n")


def validate(case, draft):
    if not isinstance(draft, dict):
        raise ValueError("draft must be a JSON object")
    card = draft.get("identity_card")
    if not isinstance(card, dict) or not isinstance(card.get("agent_description"), str) or not card["agent_description"].strip():
        raise ValueError("capability description must be retained")
    offering = card.get("offering")
    if not isinstance(offering, list) or not offering or not all(isinstance(x, str) and x.strip() for x in offering):
        raise ValueError("offering must retain supported capabilities")
    intents = draft.get("intent_actions")
    if not isinstance(intents, list) or len(intents) > 10:
        raise ValueError("intent_actions must be a list of at most 10 items")
    if not case["expects_commission_intent"]:
        if intents:
            raise ValueError("capabilities and completed work alone must not create intents")
    else:
        if not intents:
            raise ValueError("explicit ongoing commission goal must produce an intent")
        for intent in intents:
            if not isinstance(intent, dict):
                raise ValueError("intent must be an object")
            for key in ("watch_for", "trigger_when", "action_instruction"):
                if not isinstance(intent.get(key), str) or not intent[key].strip():
                    raise ValueError(f"intent requires nonempty {key}")
            if intent.get("action_policy") != "analyze_only":
                raise ValueError("this context authorizes analysis only")
    return len(intents)


def check(output):
    if json.loads((output / "source-hashes.json").read_text()) != fingerprint():
        raise ValueError("source or fixtures changed; prepare a fresh run and regenerate drafts")
    results = []
    for case in json.loads(CASES.read_text()):
        draft = json.loads((output / f'{case["id"]}.draft.json').read_text())
        count = validate(case, draft)
        results.append({"case": case["id"], "intent_count": count, "structural_checks": "passed"})
    report = {"source_hashes": fingerprint(), "cases": results, "semantic_review": "required; follow README.md"}
    (output / "checks.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "check"))
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        (prepare if args.action == "prepare" else check)(args.output)
    except (OSError, ValueError, TypeError) as error:
        parser.exit(1, f"FAIL: {error}\n")


if __name__ == "__main__":
    main()
