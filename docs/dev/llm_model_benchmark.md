# LLM Model Benchmark

Benchmark date: 2026-05-28. All tests run via Aliyun DashScope Responses API with `openai-go/v3`.

## Models Tested

| Model | Input ¥/M tokens | Output ¥/M tokens | Notes |
|-------|-------------------|---------------------|-------|
| qwen3.6-plus | 3.747 | 22.483 | Current production model |
| qwen3.6-flash | 1.874 | 11.241 | Cheapest option |
| qwen3.6-35b-a3b (MoE) | 2.810 | 16.862 | Small MoE model |
| qwen3.7-max | 18.736 | 56.207 | Best quality, highest price |
| qwen3.6-max-preview | -- | -- | Does not support Responses API |

## process_item Benchmark (reasoning=low)

### Test Cases

1. **Breaking product launch** — "OpenAI just released GPT-5 with native agent capabilities..." (EN, breaking news)
2. **Chinese policy announcement** — "中国人民银行宣布自2026年6月1日起，将存款准备金率下调0.5个百分点..." (ZH, financial alert)
3. **Evergreen tutorial** — "A comprehensive guide to building production-ready REST APIs with Go..." (EN, tutorial)
4. **Low quality spam** — "DON'T MISS OUT! Revolutionary AI tool that will 10x your productivity!" (EN, marketing spam)

### Case 1: Breaking Product Launch

| | qwen3.6-plus | qwen3.6-flash | qwen3.6-35b-a3b | qwen3.7-max |
|---|---|---|---|---|
| broadcast_type | info | info | **alert (wrong)** | info |
| timeliness | breaking | breaking | breaking | breaking |
| quality | 0.80 | 0.72 | 0.75 | 0.88 |
| expire_time | empty | empty | empty | empty |
| expected_response | none | none | none | none |
| output tokens | 2,657 | 1,976 | 2,320 | 1,216 |
| duration | 49.9s | 16.8s | 25.3s | 21.2s |

### Case 2: Chinese Policy Announcement (央行降准)

| | qwen3.6-plus | qwen3.6-flash | qwen3.6-35b-a3b | qwen3.7-max |
|---|---|---|---|---|
| broadcast_type | alert | **info (wrong)** | **info (wrong)** | alert |
| timeliness | **timely (debatable)** | **timely (debatable)** | **timely (debatable)** | breaking |
| quality | 0.85 | 0.85 | 0.88 | 0.90 |
| expire_time | empty | empty | empty | 2026-06-15 |
| expected_response | none | action | none | action |
| summary language | EN | EN | EN | **ZH (matches source)** |
| lang | zh | zh | zh | zh |
| output tokens | 2,471 | 3,238 | 3,038 | 898 |
| duration | 46.8s | 27.7s | 30.2s | 15.5s |

### Case 3: Evergreen Tutorial

All models produced correct results. No field errors.

| | qwen3.6-plus | qwen3.6-flash | qwen3.6-35b-a3b | qwen3.7-max |
|---|---|---|---|---|
| broadcast_type | info | info | info | info |
| timeliness | evergreen | evergreen | evergreen | evergreen |
| quality | 0.78 | 0.80 | 0.80 | 0.82 |
| output tokens | 1,770 | 2,250 | 1,416 | 1,222 |
| duration | 34.5s | 21.5s | 13.8s | 17.8s |

### Case 4: Low Quality Spam

All models correctly discarded (discard=true), returning minimal empty fields.

| | qwen3.6-plus | qwen3.6-flash | qwen3.6-35b-a3b | qwen3.7-max |
|---|---|---|---|---|
| discard | true | true | true | true |
| output tokens | 1,205 | 733 | 722 | 527 |
| duration | 23.3s | 7.7s | 7.8s | 11.0s |

### Cost Per Call (¥)

| Case | qwen3.6-plus | qwen3.6-flash | qwen3.6-35b-a3b | qwen3.7-max |
|------|-------------|---------------|-----------------|-------------|
| Breaking product launch | 0.0660 | 0.0253 | 0.0438 | 0.0997 |
| Chinese policy | 0.0619 | 0.0395 | 0.0559 | 0.0820 |
| Evergreen tutorial | 0.0461 | 0.0284 | 0.0286 | 0.1002 |
| Spam | 0.0334 | 0.0114 | 0.0169 | 0.0610 |
| **Average per call** | **¥0.052** | **¥0.026** | **¥0.036** | **¥0.086** |
| **Relative cost** | **2.0x** | **1.0x** | **1.4x** | **3.3x** |

### Daily Cost Estimate (10,000 items/day)

| Model | Daily Cost |
|-------|-----------|
| qwen3.6-plus | ¥520 |
| qwen3.6-flash | ¥260 |
| qwen3.6-35b-a3b | ¥360 |
| qwen3.7-max | ¥860 |

## Reasoning Effort Comparison (qwen3.7-max, process_item)

| | none | minimal | low |
|---|---|---|---|
| broadcast_type accuracy | 3/4 | 3/4 | **4/4** |
| timeliness accuracy | 4/4 | 3/4 | **4/4** |
| hallucinated expire_time | yes | yes | no |
| quality score calibration | inflated (0.9) | ok | ok |
| avg output tokens | 234 | 1,190 | 1,083 |
| avg duration | 5.5s | 19.3s | 16.4s |
| avg cost/call | ¥0.045 | ¥0.080 | ¥0.086 |
| spam handling | quality=0.9 (wrong) | quality=0.05 (not discarded) | discard=true |

`low` is the sweet spot for qwen3.7-max — `minimal` degrades broadcast_type and timeliness accuracy, `none` produces hallucinations and poor quality calibration.

## extract_keywords Benchmark (reasoning=none)

| | qwen3.6-plus | qwen3.6-flash | qwen3.6-35b-a3b | qwen3.7-max |
|---|---|---|---|---|
| Keywords quality | good | rejected words present | rejected words present | **good** |
| Respects max 10 limit | yes | **no (13 keywords)** | yes | yes |
| output tokens | 60 | 69 | 76 | 43 |
| duration | 2.0s | 1.0s | 1.2s | 1.7s |
| cost/call | ¥0.006 | ¥0.003 | ¥0.004 | ¥0.025 |

extract_keywords uses `reasoning=none` (per-prompt override) since it's simple structured extraction. All models produce acceptable results at this effort level, with qwen3.7-max and qwen3.6-plus being more precise.

## Summary

| Model | Quality | Speed | Cost | Best For |
|-------|---------|-------|------|----------|
| qwen3.7-max + low | Best judgment accuracy, fewest tokens | Fast (16.4s avg) | ¥0.086/call | Quality-critical production use |
| qwen3.6-plus + low | Stable, no broadcast errors | Slow (38.6s avg) | ¥0.052/call | Balanced reliability |
| qwen3.6-35b-a3b + low | Moderate | Fast (19.3s avg) | ¥0.036/call | Budget option with ok quality |
| qwen3.6-flash + low | broadcast_type errors on alerts | Fast (18.4s avg) | ¥0.026/call | High-volume, low-stakes processing |
