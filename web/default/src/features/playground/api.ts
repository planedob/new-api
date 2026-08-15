import { api } from '@/lib/api'
import { API_ENDPOINTS } from './constants'
import type {
  ChatCompletionRequest,
  ChatCompletionResponse,
  ModelOption,
  GroupOption,
  Image2Mode,
  Image2Request,
  Image2Response,
  Image2Result,
  Image2CapabilityCatalog,
} from './types'

/**
 * Send chat completion request (non-streaming)
 */
export async function sendChatCompletion(
  payload: ChatCompletionRequest
): Promise<ChatCompletionResponse> {
  const res = await api.post(API_ENDPOINTS.CHAT_COMPLETIONS, payload, {
    skipErrorHandler: true,
  } as Record<string, unknown>)
  return res.data
}

export async function sendImage2Request(
  request: Image2Request,
  mode: Image2Mode,
  image?: File
): Promise<Image2Result> {
  const payload = image
    ? (() => {
        const form = new FormData()
        Object.entries(request).forEach(([key, value]) =>
          form.append(key, String(value))
        )
        form.append('image', image)
        return form
      })()
    : request
  const idempotencyKey =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`
  const accepted = await api.post<{
    id: string
    poll_url?: string
    status?: string
    request_id?: string
  }>(`${API_ENDPOINTS.IMAGE2_JOBS}/${mode}`, payload, {
    headers: { 'X-Image2-Idempotency-Key': idempotencyKey },
    skipErrorHandler: true,
  } as Record<string, unknown>)
  let poll: {
    id: string
    poll_url?: string
    status?: string
    request_id?: string
    response?: Image2Response & { error?: { message?: string } }
  } = accepted.data
  const deadline = Date.now() + 15 * 60 * 1000
  while (!poll?.status || poll.status === 'processing') {
    if (Date.now() >= deadline) {
      throw new Error(
        `Image2 job ${poll?.id ?? 'unknown'} is still processing; keep the job ID and poll it again.`
      )
    }
    await new Promise((resolve) => setTimeout(resolve, 2000))
    const response = await api.get<typeof poll>(
      poll.poll_url || `${API_ENDPOINTS.IMAGE2_JOBS}/${poll.id}`,
      {
        skipErrorHandler: true,
      } as Record<string, unknown>
    )
    poll = response.data
    if (poll.status === 'failed') {
      const error = poll.response?.error?.message ?? 'Image2 request failed.'
      const failure = new Error(error) as Error & { requestId?: string }
      failure.requestId = poll.request_id ?? accepted.data.request_id
      throw failure
    }
    if (poll.status === 'succeeded') {
      const data = poll.response ?? {}
      const images = (data.data ?? [])
        .map(
          (item) =>
            item.url ??
            (item.b64_json ? `data:image/png;base64,${item.b64_json}` : '')
        )
        .filter(Boolean)
      return {
        mode,
        requestId:
          poll.request_id ?? accepted.headers['x-oneapi-request-id'] ?? null,
        images,
        error: images.length
          ? null
          : 'The image endpoint returned no image data.',
      }
    }
  }

  throw new Error('Image2 job returned an unknown status.')
}

export async function getImage2Capabilities(
  group: string,
  model = 'gpt-image-2'
): Promise<Image2CapabilityCatalog> {
  const response = await api.get<{
    success: boolean
    data?: Image2CapabilityCatalog
  }>(API_ENDPOINTS.IMAGE2_CAPABILITIES, {
    params: { group, model },
    skipErrorHandler: true,
  } as Record<string, unknown>)
  if (!response.data.success || !response.data.data) {
    throw new Error('Image2 capability metadata is unavailable.')
  }
  return response.data.data
}

/**
 * Get user available models
 */
export async function getUserModels(group?: string): Promise<ModelOption[]> {
  const res = await api.get(API_ENDPOINTS.USER_MODELS, {
    // The server resolves `auto` to the user's selectable auto candidates.
    // Omitting it would return the union of every selectable group instead.
    params: group ? { group } : undefined,
  })
  const { data } = res

  if (!data.success || !Array.isArray(data.data)) {
    return []
  }

  return data.data.map((model: string) => ({
    label: model,
    value: model,
  }))
}

/**
 * Get user groups
 */
export async function getUserGroups(): Promise<GroupOption[]> {
  const res = await api.get(API_ENDPOINTS.USER_GROUPS)
  const { data } = res

  if (!data.success || !data.data) {
    return []
  }

  const groupData = data.data as Record<string, { desc: string; ratio: number }>

  // label is for button display (name only); desc is for dropdown content
  return Object.entries(groupData).map(([group, info]) => ({
    label: group,
    value: group,
    ratio: info.ratio,
    desc: info.desc,
  }))
}
