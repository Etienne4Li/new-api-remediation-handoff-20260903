/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { PricingModel } from '../types'

// ----------------------------------------------------------------------------
// Backend metric shapes
// ----------------------------------------------------------------------------
//
// Performance and app-usage values are intentionally not inferred locally.
// The gateway is the source of truth for these values; callers must use its
// API and render an empty state until a response is available. The legacy
// builder exports below are retained as typed, empty fallbacks for consumers
// that still import them during the migration away from local mock data.

export type GroupPerformance = {
  group: string
  ttft_p50_ms: number
  ttft_p95_ms: number
  ttft_p99_ms: number
  throughput_tps: number
  uptime_30d_pct: number
  /** Number of monitored requests in the last 24h (display only). */
  request_volume_24h: number
}

export type LatencyTimePoint = {
  timestamp: string
  group: string
  ttft_ms: number
}

export type UptimeDayPoint = {
  date: string
  uptime_pct: number
  incidents: number
  outage_minutes: number
}

export type AppRanking = {
  rank: number
  name: string
  description: string
  category: string
  growth_pct: number
  monthly_tokens: number
  url?: string
  initial: string
}

const PROFILE_BY_NAME = (name: string) => {
  const n = name.toLowerCase()
  if (/embed|rerank/.test(n)) return 'embedding'
  if (/image|sora|veo|kling|pika|jimeng|dalle|imagen/.test(n)) return 'image'
  if (/whisper|tts|voice|audio/.test(n)) return 'audio'
  if (/o1|o3|o4|reasoning|thinking|deepseek-r/.test(n)) return 'reasoning'
  if (/flash|haiku|mini|small|nano|fast/.test(n)) return 'fast'
  if (/gpt-5|opus|ultra|405|70b/.test(n)) return 'large'
  return 'standard'
}

/**
 * Compatibility fallbacks for the former local metric generators.
 *
 * Metrics must come from the backend; returning an empty collection ensures
 * callers render their explicit unavailable state when no API response exists.
 */
export function buildGroupPerformance(
  _model: PricingModel
): GroupPerformance[] {
  return []
}

export function buildLatencyTimeSeries(
  _model: PricingModel
): LatencyTimePoint[] {
  return []
}

export function buildUptimeSeries(
  _model: PricingModel,
  _group?: string
): UptimeDayPoint[] {
  return []
}

export function buildAppRankings(
  _model: PricingModel,
  _count = 12
): AppRanking[] {
  return []
}

/** Compact integer formatter for token counts in apps tab. */

// ---------------------------------------------------------------------------
// Inferred supported-parameters & misc API metadata
// ---------------------------------------------------------------------------

export type SupportedParameter = {
  name: string
  type:
    | 'number'
    | 'integer'
    | 'boolean'
    | 'string'
    | 'object'
    | 'array'
    | 'enum'
  defaultValue?: string | number | boolean
  range?: string
  enumValues?: string[]
  descriptionKey: string
  required?: boolean
}

const COMMON_CHAT_PARAMS: SupportedParameter[] = [
  {
    name: 'temperature',
    type: 'number',
    defaultValue: 1,
    range: '0 ~ 2',
    descriptionKey: 'Sampling temperature; lower is more deterministic',
  },
  {
    name: 'top_p',
    type: 'number',
    defaultValue: 1,
    range: '0 ~ 1',
    descriptionKey: 'Nucleus sampling probability mass',
  },
  {
    name: 'max_tokens',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Maximum number of tokens in the response',
  },
  {
    name: 'frequency_penalty',
    type: 'number',
    defaultValue: 0,
    range: '-2 ~ 2',
    descriptionKey: 'Penalises repetition of frequent tokens',
  },
  {
    name: 'presence_penalty',
    type: 'number',
    defaultValue: 0,
    range: '-2 ~ 2',
    descriptionKey: 'Encourages introducing new topics',
  },
  {
    name: 'stop',
    type: 'array',
    descriptionKey: 'Up to 4 strings that stop generation',
  },
  {
    name: 'seed',
    type: 'integer',
    descriptionKey: 'Deterministic sampling seed (best-effort)',
  },
  {
    name: 'n',
    type: 'integer',
    defaultValue: 1,
    range: '>= 1',
    descriptionKey: 'Number of completions to generate',
  },
  {
    name: 'stream',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Stream tokens via Server-Sent Events',
  },
  {
    name: 'response_format',
    type: 'object',
    descriptionKey: 'Force JSON object or schema-conforming output',
  },
  {
    name: 'tools',
    type: 'array',
    descriptionKey: 'Tool / function declarations the model may call',
  },
  {
    name: 'tool_choice',
    type: 'string',
    enumValues: ['auto', 'none', 'required'],
    descriptionKey: 'Tool-choice policy or specific tool name',
  },
  {
    name: 'logprobs',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Return per-token log probabilities',
  },
  {
    name: 'top_logprobs',
    type: 'integer',
    range: '0 ~ 20',
    descriptionKey: 'Number of top log probabilities returned per token',
  },
  {
    name: 'logit_bias',
    type: 'object',
    descriptionKey: 'Per-token logit bias map',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const REASONING_PARAMS: SupportedParameter[] = [
  {
    name: 'reasoning_effort',
    type: 'enum',
    enumValues: ['low', 'medium', 'high'],
    defaultValue: 'medium',
    descriptionKey: 'Controls how much the model thinks before answering',
  },
  {
    name: 'max_completion_tokens',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Maximum tokens including hidden reasoning tokens',
  },
  {
    name: 'stop',
    type: 'array',
    descriptionKey: 'Up to 4 strings that stop generation',
  },
  {
    name: 'seed',
    type: 'integer',
    descriptionKey: 'Deterministic sampling seed (best-effort)',
  },
  {
    name: 'stream',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Stream tokens via Server-Sent Events',
  },
  {
    name: 'response_format',
    type: 'object',
    descriptionKey: 'Force JSON object or schema-conforming output',
  },
  {
    name: 'tools',
    type: 'array',
    descriptionKey: 'Tool / function declarations the model may call',
  },
  {
    name: 'tool_choice',
    type: 'string',
    enumValues: ['auto', 'none', 'required'],
    descriptionKey: 'Tool-choice policy or specific tool name',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const EMBEDDING_PARAMS: SupportedParameter[] = [
  {
    name: 'input',
    type: 'string',
    required: true,
    descriptionKey: 'Text or array of texts to embed',
  },
  {
    name: 'dimensions',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Truncate embeddings to this many dimensions',
  },
  {
    name: 'encoding_format',
    type: 'enum',
    enumValues: ['float', 'base64'],
    defaultValue: 'float',
    descriptionKey: 'Wire encoding for the embedding vectors',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const IMAGE_PARAMS: SupportedParameter[] = [
  {
    name: 'prompt',
    type: 'string',
    required: true,
    descriptionKey: 'Text description of the desired image',
  },
  {
    name: 'size',
    type: 'enum',
    enumValues: ['256x256', '512x512', '1024x1024', '1024x1792', '1792x1024'],
    defaultValue: '1024x1024',
    descriptionKey: 'Output image size',
  },
  {
    name: 'quality',
    type: 'enum',
    enumValues: ['standard', 'hd'],
    defaultValue: 'standard',
    descriptionKey: 'Generation quality preset',
  },
  {
    name: 'style',
    type: 'enum',
    enumValues: ['vivid', 'natural'],
    defaultValue: 'vivid',
    descriptionKey: 'Aesthetic style',
  },
  {
    name: 'n',
    type: 'integer',
    defaultValue: 1,
    range: '1 ~ 10',
    descriptionKey: 'Number of images to generate',
  },
  {
    name: 'response_format',
    type: 'enum',
    enumValues: ['url', 'b64_json'],
    defaultValue: 'url',
    descriptionKey: 'How to deliver the resulting image',
  },
]

const VIDEO_PARAMS: SupportedParameter[] = [
  {
    name: 'prompt',
    type: 'string',
    required: true,
    descriptionKey: 'Text description of the desired video',
  },
  {
    name: 'duration',
    type: 'integer',
    range: '1 ~ 60',
    descriptionKey: 'Video length in seconds',
  },
  {
    name: 'aspect_ratio',
    type: 'enum',
    enumValues: ['16:9', '9:16', '1:1'],
    defaultValue: '16:9',
    descriptionKey: 'Output aspect ratio',
  },
  {
    name: 'fps',
    type: 'integer',
    range: '8 ~ 60',
    defaultValue: 24,
    descriptionKey: 'Frames per second',
  },
]

type ApiCategory = 'reasoning' | 'embedding' | 'image' | 'video' | 'chat'

/**
 * Refine the broad PROFILE_BY_NAME bucket into an API-shape category. The
 * `image` bucket from `PROFILE_BY_NAME` lumps still-image and video models
 * together (because their performance profiles overlap); for the API tab we
 * need to distinguish them so the request-parameter table is accurate.
 */
function apiCategoryOf(model: PricingModel): ApiCategory {
  const profile = PROFILE_BY_NAME(model.model_name)
  if (profile === 'embedding' || profile === 'reasoning') return profile
  if (profile === 'image') {
    return /sora|veo|kling|pika|video|wan-|hunyuanvideo/i.test(model.model_name)
      ? 'video'
      : 'image'
  }
  return 'chat'
}

/**
 * Build the list of request parameters that the model accepts. The list is
 * shaped per-modality so reasoning, embedding, image, video and chat models
 * each show their relevant parameter set.
 */
export function buildSupportedParameters(
  model: PricingModel
): SupportedParameter[] {
  const cat = apiCategoryOf(model)
  if (cat === 'reasoning') return REASONING_PARAMS
  if (cat === 'embedding') return EMBEDDING_PARAMS
  if (cat === 'image') return IMAGE_PARAMS
  if (cat === 'video') return VIDEO_PARAMS
  return COMMON_CHAT_PARAMS
}
