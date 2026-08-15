// @ts-nocheck
import { expect, test } from 'bun:test'
import { api } from '@/lib/api'
import {
  buildImage2RequestPayload,
  getImage2ErrorDetails,
  sendImage2Request,
} from './api'
import { nextImage2ModeState } from './lib/image2-mode'

const request = {
  model: 'gpt-image-2',
  group: 'image2',
  prompt: 'safe test prompt',
  size: '1024x1024',
  quality: 'standard',
  n: 1,
}

test('default mode transition clears an edit source for generations', () => {
  const image = new File(['image'], 'reference.png', { type: 'image/png' })
  expect(nextImage2ModeState(image, 'generations')).toEqual({
    mode: 'generations',
    image: null,
  })
  expect(nextImage2ModeState(null, 'edits')).toEqual({
    mode: 'edits',
    image: null,
  })
})

test('default API generation is JSON even when a stale file is supplied', () => {
  const payload = buildImage2RequestPayload(
    request,
    'generations',
    new File(['image'], 'stale.png')
  )
  expect(payload).not.toBeInstanceOf(FormData)
  expect(payload).toEqual(request)
})

test('default API edits remains multipart and carries the source file', () => {
  const image = new File(['image'], 'reference.png', { type: 'image/png' })
  const payload = buildImage2RequestPayload(request, 'edits', image)
  expect(payload).toBeInstanceOf(FormData)
  expect(payload.get('image')).toMatchObject({
    name: image.name,
    type: image.type,
    size: image.size,
  })
  expect(payload.get('prompt')).toBe(request.prompt)
})

test('default sendImage2Request sends generation JSON despite stale image state', async () => {
  const originalPost = api.post
  const originalGet = api.get
  const originalSetTimeout = globalThis.setTimeout
  let captured: { data: unknown; config: Record<string, unknown> } | null = null
  api.post = (async (
    _url: string,
    data: unknown,
    config: Record<string, unknown>
  ) => {
    captured = { data, config }
    return {
      data: { id: 'job-1', status: 'processing' },
      headers: {},
    }
  }) as typeof api.post
  api.get = (async () => ({
    data: {
      id: 'job-1',
      status: 'succeeded',
      response: { data: [{ url: 'https://example.invalid/result.png' }] },
    },
  })) as typeof api.get
  globalThis.setTimeout = ((callback: (...args: never[]) => void) => {
    callback()
    return 0
  }) as typeof globalThis.setTimeout
  try {
    await sendImage2Request(
      request,
      'generations',
      new File(['stale'], 'stale.png')
    )
  } finally {
    api.post = originalPost
    api.get = originalGet
    globalThis.setTimeout = originalSetTimeout
  }
  expect(captured?.data).not.toBeInstanceOf(FormData)
  expect(captured?.data).toEqual(request)
})

test('default async error keeps nested message, code, metadata and request ID', () => {
  expect(
    getImage2ErrorDetails({
      response: {
        headers: { 'x-oneapi-request-id': 'req-422' },
        data: {
          error: {
            message: '完整组合不支持：size=2048x2048; quality=high',
            code: 'unsupported_image_configuration',
            metadata: { unsupported_dimensions: ['size', 'quality'] },
          },
        },
      },
    })
  ).toEqual({
    message: '完整组合不支持：size=2048x2048; quality=high',
    requestId: 'req-422',
    code: 'unsupported_image_configuration',
    metadata: { unsupported_dimensions: ['size', 'quality'] },
  })
})
